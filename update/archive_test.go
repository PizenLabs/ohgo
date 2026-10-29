package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
	mode     int64
}

func writeTarGz(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o755
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     mode,
			Size:     int64(len(entry.body)),
			Typeflag: typeflag,
			Linkname: entry.linkname,
		}
		if typeflag != tar.TypeReg && typeflag != 0 {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%q): %v", entry.name, err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatalf("Write(%q): %v", entry.name, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	path := filepath.Join(t.TempDir(), "artifact.tar.gz")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestExtractExecutableWritesOnlyTheBinary(t *testing.T) {
	path := writeTarGz(t, []tarEntry{
		{name: "ohgo-v1.0.0-linux-amd64/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "ohgo-v1.0.0-linux-amd64/README.md", body: "readme"},
		{name: "ohgo-v1.0.0-linux-amd64/ohgo", body: "#!/bin/sh\necho ohgo v1.0.0\n"},
		{name: "ohgo-v1.0.0-linux-amd64/LICENSE", body: "license"},
	})
	dest := t.TempDir()
	extracted, err := ExtractExecutable(path, dest)
	if err != nil {
		t.Fatalf("ExtractExecutable: %v", err)
	}
	body, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "echo ohgo v1.0.0") {
		t.Fatalf("extracted content = %q", body)
	}
	info, err := os.Stat(extracted)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("extracted mode = %o, want 700", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 1 {
		t.Fatalf("destination has %d entries, want only the extracted binary: %v", len(entries), entries)
	}
}

func TestExtractExecutableRejectsUnsafeArchives(t *testing.T) {
	cases := map[string][]tarEntry{
		"parent traversal": {
			{name: "root/ohgo", body: "binary"},
			{name: "../escaped", body: "evil", mode: 0o644},
		},
		"absolute path": {
			{name: "/tmp/escaped", body: "evil", mode: 0o644},
		},
		"symlink": {
			{name: "root/ohgo", body: "binary"},
			{name: "root/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		},
		"hardlink": {
			{name: "root/ohgo", body: "binary"},
			{name: "root/link", typeflag: tar.TypeLink, linkname: "root/ohgo"},
		},
		"no binary": {
			{name: "root/README.md", body: "readme"},
		},
		"duplicate binary": {
			{name: "root/ohgo", body: "one"},
			{name: "other/ohgo", body: "two"},
		},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTarGz(t, entries)
			dest := t.TempDir()
			if _, err := ExtractExecutable(path, dest); !errors.Is(err, ErrArchiveUnsafe) {
				t.Fatalf("ExtractExecutable error = %v, want ErrArchiveUnsafe", err)
			}
			entries, _ := os.ReadDir(dest)
			if len(entries) != 0 {
				t.Fatalf("unsafe archive left %d files behind", len(entries))
			}
		})
	}
}

func TestExtractExecutableRejectsEntryCountBomb(t *testing.T) {
	entries := make([]tarEntry, 0, maxArchiveEntries+1)
	for i := 0; i <= maxArchiveEntries; i++ {
		entries = append(entries, tarEntry{name: "root/README.md", body: "x"})
	}
	path := writeTarGz(t, entries)
	if _, err := ExtractExecutable(path, t.TempDir()); !errors.Is(err, ErrArchiveUnsafe) {
		t.Fatalf("entry bomb error = %v, want ErrArchiveUnsafe", err)
	}
}

func TestCheckArchiveNameAcceptsOrdinaryReleasePaths(t *testing.T) {
	for _, name := range []string{"ohgo-v1.0.0-darwin-arm64/ohgo", "ohgo-v1.0.0-darwin-arm64/README.md", "ohgo-v1.0.0-darwin-arm64/"} {
		if err := checkArchiveName(name); err != nil {
			t.Fatalf("checkArchiveName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"../ohgo", "a/../../b/ohgo", `a\..\..\b`, "/ohgo", ""} {
		if err := checkArchiveName(name); err == nil {
			t.Fatalf("checkArchiveName(%q) = nil, want rejection", name)
		}
	}
}

func TestExtractExecutableRejectsMalformedArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.tar.gz")
	if err := os.WriteFile(path, []byte("this is not gzip"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := ExtractExecutable(path, t.TempDir()); err == nil {
		t.Fatal("a malformed archive must fail")
	}
}
