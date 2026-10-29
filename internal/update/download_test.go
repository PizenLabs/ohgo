package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestDownloadToFileWritesBoundedTemporaryFile(t *testing.T) {
	const artifact = "tarball-bytes"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(artifact))
	}))
	defer server.Close()
	dest := t.TempDir()
	downloader := &Downloader{Client: server.Client(), MaxBytes: 1024}
	path, size, err := downloader.DownloadToFile(context.Background(), server.URL+"/artifact", dest)
	if err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	if size != int64(len(artifact)) {
		t.Fatalf("size = %d, want %d", size, len(artifact))
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != artifact {
		t.Fatalf("body = %q", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("download mode = %o, want 600", info.Mode().Perm())
	}
}

func TestDownloadToFileRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()
	dest := t.TempDir()
	_, _, err := (&Downloader{Client: server.Client(), MaxBytes: 100}).DownloadToFile(context.Background(), server.URL+"/artifact", dest)
	if !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("error = %v, want ErrArtifactTooLarge", err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Fatalf("oversized download left %d files", len(entries))
	}
}

func TestDownloadRejectsNonHTTPS(t *testing.T) {
	dest := t.TempDir()
	if _, _, err := (&Downloader{}).DownloadToFile(context.Background(), "http://example.test/a", dest); err == nil {
		t.Fatal("plain HTTP must be rejected")
	}
	if _, err := (&Downloader{}).FetchBytes(context.Background(), "http://example.test/a", 10); err == nil {
		t.Fatal("plain HTTP manifest fetch must be rejected")
	}
}

func TestDownloadHonorsCanceledContext(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (&Downloader{Client: server.Client()}).DownloadToFile(ctx, server.URL+"/a", t.TempDir()); err == nil {
		t.Fatal("canceled context must abort the download")
	}
}

func TestVerifyChecksums(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ohgo-v1.0.0-linux-amd64.tar.gz")
	body := []byte("artifact")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	good := []byte(digestOf(string(body)) + "  ohgo-v1.0.0-linux-amd64.tar.gz\n")
	if err := VerifyChecksums(path, "ohgo-v1.0.0-linux-amd64.tar.gz", good); err != nil {
		t.Fatalf("VerifyChecksums: %v", err)
	}
	wrong := []byte(strings.Repeat("0", 64) + "  ohgo-v1.0.0-linux-amd64.tar.gz\n")
	if err := VerifyChecksums(path, "ohgo-v1.0.0-linux-amd64.tar.gz", wrong); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	other := []byte(digestOf("x") + "  some-other-artifact.tar.gz\n")
	if err := VerifyChecksums(path, "ohgo-v1.0.0-linux-amd64.tar.gz", other); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("unlisted artifact error = %v", err)
	}
	malformed := []byte("not-a-digest  ohgo-v1.0.0-linux-amd64.tar.gz\n")
	if err := VerifyChecksums(path, "ohgo-v1.0.0-linux-amd64.tar.gz", malformed); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("malformed digest error = %v", err)
	}
}

func TestFetchBytesBoundsChecksumManifest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	}))
	defer server.Close()
	_, err := (&Downloader{Client: server.Client()}).FetchBytes(context.Background(), server.URL+"/sums", 128)
	if err == nil {
		t.Fatal("oversized manifest must be rejected")
	}
}

func TestDownloadRejectsRedirectToPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("downgraded"))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/artifact", http.StatusFound)
	}))
	defer secure.Close()
	// The injected client follows redirects by default; the response-level
	// check must still reject the final plain-HTTP hop.
	_, _, err := (&Downloader{Client: secure.Client()}).DownloadToFile(context.Background(), secure.URL+"/a", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("redirect downgrade error = %v, want an https rejection", err)
	}
}

func TestDownloadRejectsTruncatedBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()
	_, _, err := (&Downloader{Client: server.Client()}).DownloadToFile(context.Background(), server.URL+"/a", t.TempDir())
	if err == nil {
		t.Fatal("a truncated body must fail")
	}
}
