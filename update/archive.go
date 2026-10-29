package update

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// maxArchiveEntries bounds directory traversal in a release archive.
	maxArchiveEntries = 64
	// maxArchiveBytes bounds the sum of declared uncompressed entry sizes.
	// It is a bomb guard, not a normal artifact ceiling.
	maxArchiveBytes int64 = 256 << 20
)

// ErrArchiveUnsafe means an archive entry could write outside the extraction
// target or is not a plain file. The archive is rejected without extracting
// anything.
var ErrArchiveUnsafe = errors.New("update: unsafe archive")

// ExtractExecutable extracts the single "ohgo" binary from a release tarball.
//
// destDir receives a fresh temporary file and must be on the destination
// filesystem. The returned path is not yet installed or verified beyond the
// artifact checksum the caller already performed.
//
// Only a regular entry whose base name is exactly "ohgo" is extracted. Any
// absolute path, ".." element, symlink, hardlink, device, or FIFO rejects the
// whole archive: a release artifact has no legitimate reason to contain one.
func ExtractExecutable(archivePath, destDir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("extract artifact: %w", err)
	}
	defer func() { _ = file.Close() }()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("extract artifact: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()

	reader := tar.NewReader(gzipReader)
	entries := 0
	var declared int64
	var extractedPath string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanupExtract(extractedPath)
			return "", fmt.Errorf("extract artifact: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			cleanupExtract(extractedPath)
			return "", fmt.Errorf("%w: more than %d entries", ErrArchiveUnsafe, maxArchiveEntries)
		}
		if err := checkArchiveName(header.Name); err != nil {
			cleanupExtract(extractedPath)
			return "", err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, 0:
		default:
			cleanupExtract(extractedPath)
			return "", fmt.Errorf("%w: entry %q has unsupported type %q", ErrArchiveUnsafe, header.Name, header.Typeflag)
		}
		if header.Size < 0 {
			cleanupExtract(extractedPath)
			return "", fmt.Errorf("%w: entry %q has negative size", ErrArchiveUnsafe, header.Name)
		}
		declared += header.Size
		if declared > maxArchiveBytes {
			cleanupExtract(extractedPath)
			return "", fmt.Errorf("%w: archive expands beyond %d bytes", ErrArchiveUnsafe, maxArchiveBytes)
		}
		if filepath.Base(header.Name) != "ohgo" {
			// README/LICENSE are normal companions; their bytes are counted
			// above but never written.
			continue
		}
		if extractedPath != "" {
			cleanupExtract(extractedPath)
			return "", fmt.Errorf("%w: archive contains more than one ohgo binary", ErrArchiveUnsafe)
		}
		extractedPath, err = writeArchiveEntry(reader, header.Size, destDir)
		if err != nil {
			cleanupExtract(extractedPath)
			return "", err
		}
	}
	if extractedPath == "" {
		return "", fmt.Errorf("%w: archive contains no ohgo binary", ErrArchiveUnsafe)
	}
	return extractedPath, nil
}

func checkArchiveName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty entry name", ErrArchiveUnsafe)
	}
	if strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("%w: entry name contains NUL", ErrArchiveUnsafe)
	}
	// Windows separators are not path elements on Unix, but an archive
	// produced on Windows must not smuggle traversal through them.
	normalized := strings.ReplaceAll(name, `\`, "/")
	if path.IsAbs(normalized) || strings.HasPrefix(normalized, "/") {
		return fmt.Errorf("%w: absolute entry %q", ErrArchiveUnsafe, name)
	}
	if volume := filepath.VolumeName(name); volume != "" {
		return fmt.Errorf("%w: entry %q names a volume", ErrArchiveUnsafe, name)
	}
	clean := path.Clean(normalized)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: entry %q escapes the archive root", ErrArchiveUnsafe, name)
	}
	return nil
}

func writeArchiveEntry(reader io.Reader, size int64, destDir string) (string, error) {
	file, err := os.CreateTemp(destDir, ".ohgo-update-*")
	if err != nil {
		return "", fmt.Errorf("extract artifact: create temporary file: %w", err)
	}
	name := file.Name()
	fail := func(err error) (string, error) {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Chmod(0o700); err != nil {
		return fail(fmt.Errorf("extract artifact: set permissions: %w", err))
	}
	written, err := io.Copy(file, io.LimitReader(reader, size+1))
	if err != nil {
		return fail(fmt.Errorf("extract artifact: %w", err))
	}
	if written != size {
		return fail(fmt.Errorf("%w: entry is truncated", ErrArchiveUnsafe))
	}
	if err := file.Sync(); err != nil {
		return fail(fmt.Errorf("extract artifact: sync: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("extract artifact: close: %w", err)
	}
	return name, nil
}

func cleanupExtract(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
