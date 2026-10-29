package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// maxChecksumBytes bounds the checksum manifest.
	maxChecksumBytes = 1 << 20
	// DefaultMaxArtifactBytes bounds one downloaded release artifact. The
	// current tarballs are well under 40 MiB; the ceiling leaves room for
	// growth without letting a hostile endpoint fill the disk.
	DefaultMaxArtifactBytes int64 = 200 << 20
)

// ErrChecksumMismatch means the downloaded bytes do not match the published
// digest. Installation must stop: the artifact is not the one the publisher
// signed off on.
var ErrChecksumMismatch = errors.New("update: checksum mismatch")

// ErrArtifactTooLarge means a download exceeded its configured bound.
var ErrArtifactTooLarge = errors.New("update: artifact exceeds size limit")

// Downloader performs bounded HTTPS downloads.
//
// Every response is written to a caller-provided directory; nothing is kept in
// memory beyond a checksum digest.
type Downloader struct {
	Client   *http.Client
	MaxBytes int64
}

func (d *Downloader) client() *http.Client {
	if d != nil && d.Client != nil {
		return d.Client
	}
	return newHTTPSClient(DefaultDownloadTimeout)
}

// newHTTPSClient refuses a redirect that would leave TLS. A release endpoint
// is untrusted input; following it from https to http would silently downgrade
// the transport that carries the artifact.
func newHTTPSClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to non-https URL")
			}
			return nil
		},
	}
}

// ensureHTTPSResponse rejects a response whose final URL left TLS. It covers a
// caller-supplied client that did not install the redirect guard.
func ensureHTTPSResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return nil
	}
	if resp.Request.URL.Scheme != "https" {
		return fmt.Errorf("download: response arrived over %q, want https", resp.Request.URL.Scheme)
	}
	return nil
}

func (d *Downloader) maxBytes() int64 {
	if d != nil && d.MaxBytes > 0 {
		return d.MaxBytes
	}
	return DefaultMaxArtifactBytes
}

// FetchBytes downloads a small document, bounded by limit.
func (d *Downloader) FetchBytes(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("download %s: not an https URL", redactURL(rawURL))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", parsed.Host, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := d.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", parsed.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := ensureHTTPSResponse(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", parsed.Host, resp.Status)
	}
	if limit <= 0 {
		limit = maxChecksumBytes
	}
	data, err := readBounded(resp.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", parsed.Host, err)
	}
	return data, nil
}

// DownloadToFile streams a URL into a new temporary file in dir.
//
// The temp file is created with owner-only permissions and removed on every
// error path. dir is the caller's destination directory so a later rename is
// on the same filesystem.
func (d *Downloader) DownloadToFile(ctx context.Context, rawURL, dir string) (path string, size int64, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", 0, fmt.Errorf("download %s: not an https URL", redactURL(rawURL))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", 0, fmt.Errorf("download %s: %w", parsed.Host, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := d.client().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("download %s: %w", parsed.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := ensureHTTPSResponse(resp); err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("download %s: %s", parsed.Host, resp.Status)
	}
	limit := d.maxBytes()
	if resp.ContentLength > limit {
		return "", 0, fmt.Errorf("%w: %d bytes", ErrArtifactTooLarge, resp.ContentLength)
	}
	file, err := os.CreateTemp(dir, ".ohgo-update-*")
	if err != nil {
		return "", 0, fmt.Errorf("download %s: create temporary file: %w", parsed.Host, err)
	}
	path = file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(path)
		path = ""
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return "", 0, fmt.Errorf("download %s: set permissions: %w", parsed.Host, err)
	}
	written, err := io.Copy(file, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		cleanup()
		return "", 0, fmt.Errorf("download %s: %w", parsed.Host, err)
	}
	if written > limit {
		cleanup()
		return "", 0, fmt.Errorf("%w: more than %d bytes", ErrArtifactTooLarge, limit)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return "", 0, fmt.Errorf("download %s: sync: %w", parsed.Host, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		path = ""
		return "", 0, fmt.Errorf("download %s: close: %w", parsed.Host, err)
	}
	return path, written, nil
}

// VerifyChecksums authenticates artifactPath against the published manifest.
//
// The manifest is the sha256sum format the release workflow emits:
// "<64 hex>  <filename>". Only an exact name match is accepted; a manifest
// that names a different artifact cannot vouch for this one.
func VerifyChecksums(artifactPath, artifactName string, manifest []byte) error {
	want, ok := lookupChecksum(manifest, artifactName)
	if !ok {
		return fmt.Errorf("%w: %s is not listed in %s", ErrChecksumMismatch, artifactName, ChecksumAssetName)
	}
	got, err := fileSHA256(artifactPath)
	if err != nil {
		return fmt.Errorf("checksum %s: %w", artifactName, err)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: %s is %s, manifest says %s", ErrChecksumMismatch, artifactName, got, want)
	}
	return nil
}

func lookupChecksum(manifest []byte, artifactName string) (string, bool) {
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	// A manifest line is short; a pathological line is malformed input, not
	// a reason to allocate without bound.
	scanner.Buffer(make([]byte, 0, 4096), maxChecksumBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		name = pathBase(name)
		if name != artifactName {
			continue
		}
		digest := fields[0]
		if len(digest) != sha256.Size*2 || !isHex(digest) {
			return "", false
		}
		return strings.ToLower(digest), true
	}
	return "", false
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return s != ""
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// redactURL keeps a scheme and host for diagnostics without echoing a path or
// query that may contain a signed token.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "remote"
	}
	return parsed.Scheme + "://" + parsed.Host
}
