package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultRepo is the release repository ohgo publishes to.
	DefaultRepo = "PizenLabs/ohgo"
	// DefaultAPIBase is the release metadata endpoint.
	DefaultAPIBase = "https://api.github.com"
	// ChecksumAssetName is the checksum manifest published beside the
	// platform tarballs. Its presence is mandatory for a remote install.
	ChecksumAssetName = "sha256sums.txt"
	// maxMetadataBytes bounds one release-metadata response.
	maxMetadataBytes = 1 << 20
	// maxAssets bounds how many release assets a response may describe.
	maxAssets = 200
)

// ErrUnsupportedPlatform means this build's GOOS/GOARCH has no published
// artifact. It is a truthful refusal, not a failure to be retried.
var ErrUnsupportedPlatform = errors.New("update: unsupported platform")

// ErrMalformedRelease means the release source returned data this build will
// not act on: a bad tag, a non-HTTPS asset URL, a traversal-shaped filename,
// or a response that exceeds its bounds.
var ErrMalformedRelease = errors.New("update: malformed release metadata")

// Target identifies the platform an artifact must match. It is derived from
// the running binary, never from a remote filename.
type Target struct {
	GOOS   string
	GOARCH string
}

// Supported reports whether ohgo publishes a self-update artifact for the
// target. The matrix mirrors the Makefile's release matrix exactly: a platform
// missing from it must fail clearly rather than receive a lookalike binary.
func (t Target) Supported() bool {
	switch t.GOOS {
	case "linux", "darwin":
	default:
		return false
	}
	switch t.GOARCH {
	case "amd64", "arm64":
		return true
	default:
		return false
	}
}

// Asset is one file attached to a release.
type Asset struct {
	// Name is the publisher's filename, validated to be a plain base name.
	Name string
	// URL is the HTTPS download location.
	URL string
	// Size is the publisher's advertised byte count; zero means unknown.
	Size int64
}

// Release is the normalized, typed form of one release.
//
// Raw provider JSON never travels past the source boundary: the rest of the
// updater only sees values that have been validated enough to act on.
type Release struct {
	Version     Version
	Tag         string
	ID          string
	URL         string
	Prerelease  bool
	PublishedAt time.Time
	Assets      []Asset
	// ETag is the response validator used for the next conditional request.
	ETag string
}

// ArtifactPair is the verified download plan for one target.
type ArtifactPair struct {
	Archive  Asset
	Checksum Asset
}

// Source resolves the latest stable release.
//
// etag is the validator from a previous response; a source may answer
// notModified=true without a body. A not-modified answer is only meaningful
// when the caller already has the release data, which the cache tracks.
type Source interface {
	Latest(ctx context.Context, etag string) (release Release, notModified bool, err error)
}

// GitHubSource reads releases from the GitHub REST API.
//
// The client, endpoint, and repository are fields rather than package
// globals so tests can point at an httptest server and a future mirror can use
// the same source.
type GitHubSource struct {
	Client   *http.Client
	Repo     string
	Endpoint string
	// UserAgent identifies the caller as ohgo's updater.
	UserAgent string
}

// NewGitHubSource returns the production release source.
func NewGitHubSource(currentVersion string) *GitHubSource {
	ua := "ohgo"
	if v := strings.TrimSpace(currentVersion); v != "" {
		ua = "ohgo/" + v
	}
	return &GitHubSource{
		Client:    newHTTPSClient(DefaultMetadataTimeout),
		Repo:      DefaultRepo,
		Endpoint:  DefaultAPIBase,
		UserAgent: ua,
	}
}

// Latest implements Source.
func (s *GitHubSource) Latest(ctx context.Context, etag string) (Release, bool, error) {
	client := s.Client
	if client == nil {
		client = newHTTPSClient(DefaultMetadataTimeout)
	}
	repo := strings.TrimSpace(s.Repo)
	if repo == "" {
		repo = DefaultRepo
	}
	endpoint := strings.TrimRight(strings.TrimSpace(s.Endpoint), "/")
	if endpoint == "" {
		endpoint = DefaultAPIBase
	}
	requestURL := endpoint + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return Release{}, false, fmt.Errorf("release metadata request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if ua := strings.TrimSpace(s.UserAgent); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, false, fmt.Errorf("release metadata request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := ensureHTTPSResponse(resp); err != nil {
		return Release{}, false, err
	}
	switch resp.StatusCode {
	case http.StatusNotModified:
		return Release{}, true, nil
	case http.StatusOK:
	default:
		return Release{}, false, fmt.Errorf("release metadata: %s", resp.Status)
	}

	body, err := readBounded(resp.Body, maxMetadataBytes)
	if err != nil {
		return Release{}, false, fmt.Errorf("release metadata: %w", err)
	}
	release, err := parseGitHubRelease(body)
	if err != nil {
		return Release{}, false, err
	}
	release.ETag = strings.TrimSpace(resp.Header.Get("ETag"))
	return release, false, nil
}

// githubRelease is the provider wire shape. It is private: only fields the
// updater uses are decoded, so an unexpected field can never flow onward.
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	ID          int64         `json:"id"`
	HTMLURL     string        `json:"html_url"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func parseGitHubRelease(body []byte) (Release, error) {
	var wire githubRelease
	if err := json.Unmarshal(body, &wire); err != nil {
		return Release{}, fmt.Errorf("%w: decode release: %v", ErrMalformedRelease, err)
	}
	if wire.Draft {
		return Release{}, fmt.Errorf("%w: release is a draft", ErrMalformedRelease)
	}
	tag := strings.TrimSpace(wire.TagName)
	version, ok := ParseVersion(tag)
	if !ok {
		return Release{}, fmt.Errorf("%w: tag %q is not a version", ErrMalformedRelease, tag)
	}
	releaseURL, err := validateHTTPSURL(wire.HTMLURL, "release page")
	if err != nil {
		return Release{}, err
	}
	if len(wire.Assets) > maxAssets {
		return Release{}, fmt.Errorf("%w: %d assets exceeds limit", ErrMalformedRelease, len(wire.Assets))
	}
	assets := make([]Asset, 0, len(wire.Assets))
	for _, raw := range wire.Assets {
		name := strings.TrimSpace(raw.Name)
		if name == "" || name != pathBase(name) {
			return Release{}, fmt.Errorf("%w: asset name %q is not a base name", ErrMalformedRelease, raw.Name)
		}
		assetURL, err := validateHTTPSURL(raw.BrowserDownloadURL, "asset "+name)
		if err != nil {
			return Release{}, err
		}
		if raw.Size < 0 {
			// A negative size is meaningless and could short-circuit a size
			// guard; treat it as a malformed response rather than "unknown".
			return Release{}, fmt.Errorf("%w: asset %q has negative size", ErrMalformedRelease, name)
		}
		assets = append(assets, Asset{Name: name, URL: assetURL, Size: raw.Size})
	}
	return Release{
		Version:     version,
		Tag:         tag,
		ID:          fmt.Sprintf("%d", wire.ID),
		URL:         releaseURL,
		Prerelease:  wire.Prerelease,
		PublishedAt: wire.PublishedAt,
		Assets:      assets,
	}, nil
}

// pathBase is filepath.Base without importing path semantics for a value that
// must not contain either separator.
func pathBase(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		return name[i+1:]
	}
	return name
}

func validateHTTPSURL(raw, what string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", fmt.Errorf("%w: %s URL is empty", ErrMalformedRelease, what)
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("%w: %s URL is not https", ErrMalformedRelease, what)
	}
	parsed.User = nil
	return parsed.String(), nil
}

// ArtifactFor selects the archive and checksum assets for a target.
//
// Selection is by exact expected filename. A remote filename is never
// consulted for platform identity: the expected name is derived from the
// release tag and the *local* GOOS/GOARCH, so a mislabelled artifact cannot
// redirect the update to another platform.
func (r Release) ArtifactFor(target Target) (ArtifactPair, error) {
	if !target.Supported() {
		return ArtifactPair{}, fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, target.GOOS, target.GOARCH)
	}
	if r.Prerelease {
		return ArtifactPair{}, fmt.Errorf("%w: release %s is a prerelease", ErrMalformedRelease, r.Tag)
	}
	expected := fmt.Sprintf("ohgo-%s-%s-%s.tar.gz", r.Tag, target.GOOS, target.GOARCH)
	var archive Asset
	var checksum Asset
	for _, asset := range r.Assets {
		switch asset.Name {
		case expected:
			if archive.Name == "" {
				archive = asset
			}
		case ChecksumAssetName:
			if checksum.Name == "" {
				checksum = asset
			}
		}
	}
	if archive.Name == "" {
		return ArtifactPair{}, fmt.Errorf("%w: release %s has no artifact %q", ErrMalformedRelease, r.Tag, expected)
	}
	if checksum.Name == "" {
		// The release workflow always publishes this manifest. Its absence
		// means the artifact cannot be authenticated, so installation must
		// stop rather than proceed with integrity unverified.
		return ArtifactPair{}, fmt.Errorf("%w: release %s has no %s", ErrMalformedRelease, r.Tag, ChecksumAssetName)
	}
	return ArtifactPair{Archive: archive, Checksum: checksum}, nil
}

// readBounded reads at most limit bytes and fails when the source is larger.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}
