package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTargetSupportedMatchesReleaseMatrix(t *testing.T) {
	supported := []Target{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
	}
	for _, target := range supported {
		if !target.Supported() {
			t.Fatalf("%s/%s should be supported", target.GOOS, target.GOARCH)
		}
	}
	unsupported := []Target{
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "386"},
		{GOOS: "freebsd", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "riscv64"},
	}
	for _, target := range unsupported {
		if target.Supported() {
			t.Fatalf("%s/%s should be unsupported", target.GOOS, target.GOARCH)
		}
	}
}

func TestGitHubSourceResolvesNormalizedReleaseAndConditionalRequest(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == `"etag-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"etag-1"`)
		_, _ = w.Write([]byte(`{
			"tag_name": "v1.2.3",
			"id": 7,
			"html_url": "https://github.com/PizenLabs/ohgo/releases/tag/v1.2.3",
			"draft": false,
			"prerelease": false,
			"published_at": "2026-10-01T12:00:00Z",
			"assets": [
				{"name":"ohgo-v1.2.3-linux-amd64.tar.gz","browser_download_url":"https://example.test/ohgo-linux.tar.gz","size":10},
				{"name":"sha256sums.txt","browser_download_url":"https://example.test/sha256sums.txt","size":100}
			]
		}`))
	}))
	defer server.Close()
	source := &GitHubSource{Client: server.Client(), Repo: "PizenLabs/ohgo", Endpoint: server.URL, UserAgent: "ohgo/test"}
	release, notModified, err := source.Latest(context.Background(), "")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if notModified {
		t.Fatal("first request unexpectedly not modified")
	}
	if release.Tag != "v1.2.3" || release.ID != "7" || release.ETag != `"etag-1"` {
		t.Fatalf("release = %+v", release)
	}
	if len(release.Assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(release.Assets))
	}
	if _, notModified, err = source.Latest(context.Background(), `"etag-1"`); err != nil || !notModified {
		t.Fatalf("conditional request = (%v, %v), want not modified", notModified, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestGitHubSourceRejectsUntrustworthyMetadata(t *testing.T) {
	cases := map[string]string{
		"http asset": `{"tag_name":"v1.0.0","html_url":"https://example.test/r","assets":[{"name":"sha256sums.txt","browser_download_url":"http://example.test/s","size":1}]}`,
		"path asset": `{"tag_name":"v1.0.0","html_url":"https://example.test/r","assets":[{"name":"../evil","browser_download_url":"https://example.test/s","size":1}]}`,
		"bad tag":    `{"tag_name":"latest","html_url":"https://example.test/r"}`,
		"draft":      `{"tag_name":"v1.0.0","draft":true,"html_url":"https://example.test/r"}`,
		"bad page":   `{"tag_name":"v1.0.0","html_url":"file:///etc/passwd"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitHubRelease([]byte(body)); !errors.Is(err, ErrMalformedRelease) {
				t.Fatalf("parseGitHubRelease error = %v, want ErrMalformedRelease", err)
			}
		})
	}
}

func TestArtifactForUsesLocalTargetNotRemoteName(t *testing.T) {
	release := Release{
		Version: Version{Major: 1, Raw: "v1.0.0"},
		Tag:     "v1.0.0",
		Assets: []Asset{
			{Name: "ohgo-v1.0.0-linux-amd64.tar.gz", URL: "https://example.test/linux"},
			{Name: ChecksumAssetName, URL: "https://example.test/sums"},
		},
	}
	if _, err := release.ArtifactFor(Target{GOOS: "darwin", GOARCH: "arm64"}); !errors.Is(err, ErrMalformedRelease) {
		t.Fatalf("a linux asset must not satisfy a darwin target: %v", err)
	}
	pair, err := release.ArtifactFor(Target{GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatalf("ArtifactFor: %v", err)
	}
	if pair.Archive.Name != "ohgo-v1.0.0-linux-amd64.tar.gz" || pair.Checksum.Name != ChecksumAssetName {
		t.Fatalf("pair = %+v", pair)
	}
}

func TestArtifactForRequiresChecksumsAndSupportedPlatform(t *testing.T) {
	release := Release{
		Tag: "v1.0.0",
		Assets: []Asset{
			{Name: "ohgo-v1.0.0-darwin-arm64.tar.gz", URL: "https://example.test/darwin"},
		},
	}
	if _, err := release.ArtifactFor(Target{GOOS: "darwin", GOARCH: "arm64"}); !errors.Is(err, ErrMalformedRelease) {
		t.Fatalf("missing checksum manifest must fail closed: %v", err)
	}
	if _, err := release.ArtifactFor(Target{GOOS: "windows", GOARCH: "amd64"}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("windows must be unsupported: %v", err)
	}
}

func TestGitHubSourceSurfacesHTTPError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	source := &GitHubSource{Client: server.Client(), Endpoint: server.URL}
	if _, _, err := source.Latest(context.Background(), ""); err == nil {
		t.Fatal("a 500 response must be an error")
	}
}

func TestParseGitHubReleaseBoundsAssetCount(t *testing.T) {
	body := map[string]any{
		"tag_name": "v1.0.0",
		"html_url": "https://example.test/r",
		"assets":   make([]any, maxAssets+1),
	}
	raw, _ := json.Marshal(body)
	if _, err := parseGitHubRelease(raw); !errors.Is(err, ErrMalformedRelease) {
		t.Fatalf("oversized asset list error = %v", err)
	}
}
