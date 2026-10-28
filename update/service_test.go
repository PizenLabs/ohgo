package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type staticSource struct {
	release     Release
	err         error
	notModified bool
	calls       int
	etag        string
}

func (s *staticSource) Latest(_ context.Context, etag string) (Release, bool, error) {
	s.calls++
	s.etag = etag
	if s.err != nil {
		return Release{}, false, s.err
	}
	return s.release, s.notModified, nil
}

func tarGzBytes(t *testing.T, name, body string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	payload := []byte(body)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "ohgo-release/" + name,
		Mode:     0o755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

type updateFixture struct {
	service *Service
	source  *staticSource
	dest    string
	server  *httptest.Server
	tag     string
}

func newUpdateFixture(t *testing.T, current, tag, binary string, manifest func(name string, archive []byte) []byte) *updateFixture {
	t.Helper()
	target := Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if !target.Supported() {
		t.Skipf("unsupported test platform %s/%s", target.GOOS, target.GOARCH)
	}
	archive := tarGzBytes(t, "ohgo", binary)
	name := fmt.Sprintf("ohgo-%s-%s-%s.tar.gz", tag, target.GOOS, target.GOARCH)
	if manifest == nil {
		sum := sha256.Sum256(archive)
		manifest = func(string, []byte) []byte {
			return []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
		}
	}
	sums := manifest(name, archive)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/archive":
			_, _ = w.Write(archive)
		case "/sums":
			_, _ = w.Write(sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	dest := filepath.Join(root, "bin", "ohgo")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dest, []byte("#!/bin/sh\necho ohgo v1.0.0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile destination: %v", err)
	}
	version, ok := ParseVersion(tag)
	if !ok {
		t.Fatalf("bad test tag %q", tag)
	}
	source := &staticSource{release: Release{
		Version: version,
		Tag:     tag,
		ID:      "1",
		URL:     "https://example.test/release",
		Assets: []Asset{
			{Name: name, URL: server.URL + "/archive", Size: int64(len(archive))},
			{Name: ChecksumAssetName, URL: server.URL + "/sums", Size: int64(len(sums))},
		},
	}}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{
		CurrentVersion: current,
		GOOS:           target.GOOS,
		GOARCH:         target.GOARCH,
		Source:         source,
		Cache:          NewCacheStoreAt(filepath.Join(root, "update.json")),
		Downloader:     &Downloader{Client: server.Client()},
		Installer:      &Installer{},
		Now:            func() time.Time { return now },
		CheckInterval:  time.Hour,
		Executable:     func() (string, error) { return dest, nil },
		LockPath:       filepath.Join(root, "update.lock"),
	})
	return &updateFixture{service: service, source: source, dest: dest, server: server, tag: tag}
}

func TestServiceUpdateInstallsVerifiedRelease(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	result, err := fixture.service.Update(context.Background())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.AlreadyCurrent || result.InstalledVersion != "v2.0.0" {
		t.Fatalf("result = %+v", result)
	}
	got, err := InstalledVersion(context.Background(), fixture.dest)
	if err != nil {
		t.Fatalf("InstalledVersion: %v", err)
	}
	if got != "v2.0.0" {
		t.Fatalf("installed version = %q", got)
	}
	cache, ok := fixture.service.cache.Load()
	if !ok || cache.LatestVersion != "v2.0.0" {
		t.Fatalf("cache = %+v, ok=%v", cache, ok)
	}
	if _, err := os.Stat(backupPath(fixture.dest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup left after verified install: %v", err)
	}
}

func TestServiceUpdateDoesNotDownloadWhenAlreadyCurrent(t *testing.T) {
	fixture := newUpdateFixture(t, "v2.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	result, err := fixture.service.Update(context.Background())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !result.AlreadyCurrent {
		t.Fatalf("result = %+v, want already current", result)
	}
	body, err := os.ReadFile(fixture.dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "#!/bin/sh\necho ohgo v1.0.0\n" {
		t.Fatalf("destination changed on a no-op update: %q", body)
	}
}

func TestServiceUpdateRefusesChecksumMismatchBeforeExtraction(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n",
		func(name string, _ []byte) []byte {
			return []byte(fmt.Sprintf("%064d  %s\n", 0, name))
		})
	_, err := fixture.service.Update(context.Background())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Update error = %v, want ErrChecksumMismatch", err)
	}
	body, readErr := os.ReadFile(fixture.dest)
	if readErr != nil || string(body) != "#!/bin/sh\necho ohgo v1.0.0\n" {
		t.Fatalf("destination changed after checksum failure: %q, %v", body, readErr)
	}
}

func TestServiceUpdateStopsWhenArtifactVerificationFails(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	sentinel := errors.New("signature verification failed")
	fixture.service.verify = func(context.Context, string, string, []byte) error { return sentinel }
	_, err := fixture.service.Update(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v, want the verifier failure", err)
	}
	body, readErr := os.ReadFile(fixture.dest)
	if readErr != nil || string(body) != "#!/bin/sh\necho ohgo v1.0.0\n" {
		t.Fatalf("destination changed after verification failure: %q, %v", body, readErr)
	}
}

func TestServiceObserveReportsUnsupportedPlatformWithoutAvailability(t *testing.T) {
	service := NewService(Config{
		CurrentVersion: "v1.0.0",
		GOOS:           "plan9",
		GOARCH:         "amd64",
		Source:         &staticSource{},
		Cache:          NewCacheStoreAt(filepath.Join(t.TempDir(), "update.json")),
	})
	status := service.Observe()
	if status.Available || status.PlatformSupported || status.Reason != ReasonUnsupported {
		t.Fatalf("Observe = %+v, want unsupported and unavailable", status)
	}
	check, err := service.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if check.Available || check.Reason != ReasonUnsupported {
		t.Fatalf("Check = %+v, want unsupported and unavailable", check)
	}
}

func TestServiceUpdateRollsBackWhenNewBinaryReportsTheWrongVersion(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v1.5.0\n", nil)
	_, err := fixture.service.Update(context.Background())
	if !errors.Is(err, ErrInstallVerification) {
		t.Fatalf("Update error = %v, want ErrInstallVerification", err)
	}
	got, probeErr := InstalledVersion(context.Background(), fixture.dest)
	if probeErr != nil {
		t.Fatalf("InstalledVersion after rollback: %v", probeErr)
	}
	if got != "v1.0.0" {
		t.Fatalf("destination after rollback reports %q, want v1.0.0", got)
	}
}

func TestServiceUpdateRefusesDevelopmentBuildWithoutNetwork(t *testing.T) {
	fixture := newUpdateFixture(t, "0.1.0-dev", "v9.0.0", "#!/bin/sh\necho ohgo v9.0.0\n", nil)
	_, err := fixture.service.Update(context.Background())
	if !errors.Is(err, ErrDevelopmentVersion) {
		t.Fatalf("Update error = %v, want ErrDevelopmentVersion", err)
	}
	if fixture.source.calls != 0 {
		t.Fatalf("development build performed %d release calls", fixture.source.calls)
	}
}

func TestServiceUpdateRefusesUnsupportedPlatformWithoutNetwork(t *testing.T) {
	source := &staticSource{}
	service := NewService(Config{
		CurrentVersion: "v1.0.0",
		GOOS:           "windows",
		GOARCH:         "amd64",
		Source:         source,
		Cache:          NewCacheStoreAt(filepath.Join(t.TempDir(), "update.json")),
		Executable:     func() (string, error) { return filepath.Join(t.TempDir(), "ohgo"), nil },
	})
	if _, err := service.Update(context.Background()); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Update error = %v, want ErrUnsupportedPlatform", err)
	}
	if source.calls != 0 {
		t.Fatalf("unsupported platform performed %d release calls", source.calls)
	}
}

func TestServiceUpdateRefusesPackageManagedInstall(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	cellar := filepath.Join(filepath.Dir(filepath.Dir(fixture.dest)), "Cellar", "ohgo", "1.0.0")
	if err := os.MkdirAll(filepath.Join(cellar, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellar, "INSTALL_RECEIPT.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile receipt: %v", err)
	}
	managed := filepath.Join(cellar, "bin", "ohgo")
	if err := os.WriteFile(managed, []byte("#!/bin/sh\necho ohgo v1.0.0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile managed: %v", err)
	}
	fixture.service.executable = func() (string, error) { return managed, nil }
	_, err := fixture.service.Update(context.Background())
	var managedErr *ManagedError
	if !errors.As(err, &managedErr) || managedErr.Manager != "Homebrew" {
		t.Fatalf("Update error = %v, want ManagedError for Homebrew", err)
	}
	body, readErr := os.ReadFile(managed)
	if readErr != nil || string(body) != "#!/bin/sh\necho ohgo v1.0.0\n" {
		t.Fatalf("managed binary changed: %q, %v", body, readErr)
	}
}

func TestServiceUpdateRefusesConcurrentInstaller(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	held, err := acquireLock(fixture.service.lockPath, fixture.service.now)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	defer held.release()
	if _, err := fixture.service.Update(context.Background()); !errors.Is(err, ErrUpdateBusy) {
		t.Fatalf("Update error = %v, want ErrUpdateBusy", err)
	}
}

func TestServiceObserveAndCheckUseTheCache(t *testing.T) {
	fixture := newUpdateFixture(t, "v1.0.0", "v2.0.0", "#!/bin/sh\necho ohgo v2.0.0\n", nil)
	if status := fixture.service.Observe(); status.Available || status.Cached {
		t.Fatalf("empty cache observed as %+v", status)
	}
	status, err := fixture.service.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !status.Available || status.LatestVersion != "v2.0.0" || status.Stale {
		t.Fatalf("Check status = %+v", status)
	}
	observed := fixture.service.Observe()
	if !observed.Available || !observed.Cached || observed.Stale {
		t.Fatalf("Observe status = %+v", observed)
	}
}

func TestServiceCheckSendsCachedETagAndAcceptsNotModified(t *testing.T) {
	root := t.TempDir()
	store := NewCacheStoreAt(filepath.Join(root, "update.json"))
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if err := store.Save(Cache{
		LatestVersion: "v2.0.0",
		ReleaseID:     "5",
		ReleaseURL:    "https://example.test/r",
		ETag:          `"v2"`,
		CheckedAt:     now.Add(-48 * time.Hour),
		LastAttemptAt: now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	source := &staticSource{notModified: true}
	service := NewService(Config{
		CurrentVersion: "v1.0.0",
		Source:         source,
		Cache:          store,
		Now:            func() time.Time { return now },
		CheckInterval:  time.Hour,
	})
	status, err := service.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if source.etag != `"v2"` {
		t.Fatalf("conditional ETag = %q, want cached validator", source.etag)
	}
	if !status.Available || status.LatestVersion != "v2.0.0" {
		t.Fatalf("status = %+v", status)
	}
	cache, _ := store.Load()
	if cache.ETag != `"v2"` || !cache.LastAttemptAt.Equal(now) {
		t.Fatalf("cache after 304 = %+v", cache)
	}
}

func TestServiceCheckFailurePreservesLastObservation(t *testing.T) {
	root := t.TempDir()
	store := NewCacheStoreAt(filepath.Join(root, "update.json"))
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if err := store.Save(Cache{
		LatestVersion: "v2.0.0",
		CheckedAt:     now.Add(-48 * time.Hour),
		LastAttemptAt: now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	source := &staticSource{err: errors.New("network down")}
	service := NewService(Config{
		CurrentVersion: "v1.0.0",
		Source:         source,
		Cache:          store,
		Now:            func() time.Time { return now },
		CheckInterval:  time.Hour,
	})
	status, err := service.Check(context.Background())
	if err == nil {
		t.Fatal("Check must surface the failure")
	}
	if !status.Available || status.LatestVersion != "v2.0.0" {
		t.Fatalf("last observation was lost: %+v", status)
	}
	if status.Stale {
		t.Fatal("a failed attempt must suppress an immediate retry for one TTL")
	}
	cache, _ := store.Load()
	if cache.LastError == "" || !cache.LastAttemptAt.Equal(now) {
		t.Fatalf("cache after failure = %+v", cache)
	}
	if !cache.CheckedAt.Equal(now.Add(-48 * time.Hour)) {
		t.Fatalf("failed attempt rewrote the accepted observation: %+v", cache)
	}
}

func TestManagedErrorExplainsTheOwningManager(t *testing.T) {
	withCommand := (&ManagedError{Manager: "Homebrew", Command: "brew upgrade ohgo"}).Error()
	if withCommand == "" || !bytes.Contains([]byte(withCommand), []byte("brew upgrade ohgo")) {
		t.Fatalf("managed error = %q", withCommand)
	}
	withoutCommand := (&ManagedError{Manager: "Nix"}).Error()
	if withoutCommand == "" || bytes.Contains([]byte(withoutCommand), []byte("`")) {
		t.Fatalf("managed error fabricated a command: %q", withoutCommand)
	}
}
