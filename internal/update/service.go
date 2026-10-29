package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/PizenLabs/ohgo/internal/state"
)

// Platform defaults mirror the Makefile's release matrix.
const (
	// DefaultCheckInterval is how long a successful or failed observation is
	// reused before another background check is allowed.
	DefaultCheckInterval = 24 * time.Hour
	// DefaultMetadataTimeout bounds one release-metadata request.
	DefaultMetadataTimeout = 10 * time.Second
	// DefaultDownloadTimeout bounds one artifact download.
	DefaultDownloadTimeout = 2 * time.Minute
	// DefaultVerifyTimeout bounds the post-install version probe.
	DefaultVerifyTimeout = 10 * time.Second
)

// ErrUpdateBusy means another ohgo process holds the installation lock.
// Concurrent updates are refused rather than allowed to race over the same
// destination.
var ErrUpdateBusy = errors.New("update: another update is already running")

// ErrLockUnavailable means no safe location for the installation lock could be
// resolved. The updater refuses rather than proceeding without exclusion.
var ErrLockUnavailable = errors.New("update: cannot create an installation lock")

// ErrDevelopmentVersion means the running build has no orderable release
// identity. An explicit update refuses rather than guessing.
var ErrDevelopmentVersion = errors.New("update: current build is not a release")

// ErrNoExecutable means the running executable path could not be resolved.
var ErrNoExecutable = errors.New("update: cannot resolve running executable")

// Stage names one observable phase of an explicit update.
type Stage string

const (
	// StageResolving resolves release metadata.
	StageResolving Stage = "resolving release"
	// StageDownloading fetches the artifact and checksum manifest.
	StageDownloading Stage = "downloading"
	// StageVerifyingArtifact authenticates the downloaded artifact.
	StageVerifyingArtifact Stage = "verifying artifact"
	// StageExtracting extracts the single executable.
	StageExtracting Stage = "extracting"
	// StageInstalling replaces the installed executable.
	StageInstalling Stage = "installing"
	// StageVerifyingInstallation proves the installed binary is the requested
	// release.
	StageVerifyingInstallation Stage = "verifying installation"
)

// VerifyFunc authenticates a downloaded artifact before extraction.
//
// The default verifies the SHA-256 in the publisher's manifest. The seam is
// deliberately shaped so a release-signature verifier can replace or augment
// it without changing download, extraction, or installation.
type VerifyFunc func(ctx context.Context, artifactPath, artifactName string, manifest []byte) error

// Config wires a Service. Zero values select production defaults, so a caller
// only sets the seam a test substitutes.
type Config struct {
	// CurrentVersion is the build's version, normally main.version.
	CurrentVersion string
	// GOOS and GOARCH default to the running platform.
	GOOS   string
	GOARCH string
	// Source resolves releases. Default: GitHubSource for DefaultRepo.
	Source Source
	// Cache is the local observation store. Default: the machine state path.
	Cache *CacheStore
	// Transition is the local lifecycle store. Default: the machine state
	// path. A successful explicit update records a pending post-update
	// notice here; release observation never writes it.
	Transition *TransitionStore
	// Downloader defaults to a bounded HTTPS client.
	Downloader *Downloader
	// Installer defaults to the production transaction.
	Installer *Installer
	// Now supplies the clock. Default: time.Now.
	Now func() time.Time
	// CheckInterval is the cache TTL. Zero uses DefaultCheckInterval; a
	// negative value makes every launch stale.
	CheckInterval time.Duration
	// Progress receives each explicit-update stage. Nil is silent.
	Progress func(Stage)
	// VerifyArtifact overrides checksum verification. Nil uses the published
	// manifest; a caller must not pass a no-op in production.
	VerifyArtifact VerifyFunc
	// Executable resolves the installed binary path. Default: os.Executable
	// with symlinks resolved.
	Executable func() (string, error)
	// LockPath is the installation lock file. Default: the machine state
	// directory.
	LockPath string
}

// Status is the local view of release availability.
type Status struct {
	CurrentVersion string
	LatestVersion  string
	// Available is true only when the latest release is a legitimate,
	// non-downgrade update under UpdateAvailable's policy.
	Available bool
	// Reason explains the decision.
	Reason DecisionReason
	// CheckedAt is when the cached observation was accepted.
	CheckedAt time.Time
	// Cached reports that this status came from the local cache.
	Cached bool
	// ReleaseURL is provenance for the cached observation.
	ReleaseURL string
	// Stale reports the cache is old enough for a refresh.
	Stale bool
	// PlatformSupported reports whether this build could install a release
	// at all. An unsupported platform never exposes availability.
	PlatformSupported bool
}

// Result is the outcome of an explicit update.
type Result struct {
	// AlreadyCurrent means no installation was attempted.
	AlreadyCurrent bool
	// InstalledVersion is the release now on disk, when one was installed.
	InstalledVersion string
	// Path is the installed executable.
	Path string
	// Status is the observation that drove the result.
	Status Status
}

// ManagedError reports a provably package-manager-owned installation.
type ManagedError struct {
	Path    string
	Manager string
	Command string
}

func (e *ManagedError) Error() string {
	if e.Command != "" {
		return fmt.Sprintf("installation is managed by %s; use %q to upgrade it", e.Manager, e.Command)
	}
	return fmt.Sprintf("installation is managed by %s; upgrade it with that package manager", e.Manager)
}

// Service is the single update authority.
type Service struct {
	current    string
	target     Target
	source     Source
	cache      *CacheStore
	transition *TransitionStore
	downloader *Downloader
	installer  *Installer
	now        func() time.Time
	ttl        time.Duration
	progress   func(Stage)
	verify     VerifyFunc
	executable func() (string, error)
	lockPath   string
}

// NewService returns a Service with production defaults filled in.
func NewService(cfg Config) *Service {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	goos, goarch := cfg.GOOS, cfg.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	source := cfg.Source
	if source == nil {
		source = NewGitHubSource(cfg.CurrentVersion)
	}
	cache := cfg.Cache
	if cache == nil {
		if store, err := NewCacheStore(); err == nil {
			cache = store
		}
	}
	transition := cfg.Transition
	if transition == nil {
		if store, err := NewTransitionStore(); err == nil {
			transition = store
		}
	}
	ttl := cfg.CheckInterval
	if ttl == 0 {
		ttl = DefaultCheckInterval
	}
	lockPath := cfg.LockPath
	if lockPath == "" {
		if path, err := machineLockPath(); err == nil {
			lockPath = path
		}
	}
	installer := cfg.Installer
	if installer == nil {
		installer = &Installer{}
	}
	downloader := cfg.Downloader
	if downloader == nil {
		downloader = &Downloader{}
	}
	executable := cfg.Executable
	if executable == nil {
		executable = resolveExecutable
	}
	return &Service{
		current:    cfg.CurrentVersion,
		target:     Target{GOOS: goos, GOARCH: goarch},
		source:     source,
		cache:      cache,
		transition: transition,
		downloader: downloader,
		installer:  installer,
		now:        now,
		ttl:        ttl,
		progress:   cfg.Progress,
		verify:     cfg.VerifyArtifact,
		executable: executable,
		lockPath:   lockPath,
	}
}

// Observe returns the local cache view without touching the network. It is
// what a normal invocation may read on its startup path.
func (s *Service) Observe() Status {
	status := Status{
		CurrentVersion:    s.current,
		Reason:            ReasonNotChecked,
		PlatformSupported: s.target.Supported(),
	}
	if !status.PlatformSupported {
		status.Reason = ReasonUnsupported
	} else if cache, ok := s.loadCache(); ok {
		status.LatestVersion = cache.LatestVersion
		status.CheckedAt = cache.CheckedAt
		status.Cached = true
		status.ReleaseURL = cache.ReleaseURL
		if cache.LatestVersion == "" {
			status.Reason = ReasonNotChecked
		} else {
			status.Available, status.Reason = UpdateAvailable(s.current, cache.LatestVersion)
		}
		status.Stale = !Fresh(cache, s.ttl, s.now())
	} else if IsDevelopment(s.current) {
		status.Reason = ReasonDevelopment
	}
	return status
}

// Check refreshes the release observation.
//
// A conditional request is used when the cache carries an ETag. Any failure
// leaves the previous observation intact and records the attempt so the TTL
// suppresses an immediate retry on the next launch.
func (s *Service) Check(ctx context.Context) (Status, error) {
	if IsDevelopment(s.current) {
		return Status{CurrentVersion: s.current, Reason: ReasonDevelopment, PlatformSupported: s.target.Supported()}, nil
	}
	if !s.target.Supported() {
		return Status{CurrentVersion: s.current, Reason: ReasonUnsupported, PlatformSupported: false}, nil
	}
	release, cache, notModified, err := s.resolveLatest(ctx, true)
	now := s.now().UTC()
	if err != nil {
		cache.LastAttemptAt = now
		cache.LastError = err.Error()
		s.saveCache(cache)
		status := s.statusFrom(cache)
		return status, err
	}
	cache.LatestVersion = release.Tag
	cache.ReleaseID = release.ID
	cache.ReleaseURL = release.URL
	cache.ETag = release.ETag
	cache.CheckedAt = now
	cache.LastAttemptAt = now
	cache.LastError = ""
	s.saveCache(cache)
	status := s.statusFrom(cache)
	status.Cached = notModified
	return status, nil
}

// Update performs the complete explicit path. The caller has already supplied
// human authority by invoking the command; the method still refuses every
// condition that would make the replacement unsafe.
func (s *Service) Update(ctx context.Context) (Result, error) {
	if IsDevelopment(s.current) {
		return Result{}, fmt.Errorf("%w: %s", ErrDevelopmentVersion, s.current)
	}
	if !s.target.Supported() {
		return Result{}, fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, s.target.GOOS, s.target.GOARCH)
	}
	// Ownership is resolved before any network request: a package-manager
	// installation must learn its owner even when the machine is offline.
	executable, err := s.executable()
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrNoExecutable, err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrNoExecutable, err)
	}
	if manager := DetectManager(executable); manager.Managed {
		return Result{}, &ManagedError{
			Path:    executable,
			Manager: manager.Name,
			Command: manager.Command,
		}
	}
	if s.progress != nil {
		s.progress(StageResolving)
	}
	// An explicit update deliberately ignores the conditional validator: it
	// needs the artifact list, which the cache does not store. Metadata is
	// small and this path is rare.
	release, cache, _, err := s.resolveLatest(ctx, false)
	if err != nil {
		return Result{}, err
	}
	status := s.statusFrom(cache)
	status.LatestVersion = release.Tag
	status.ReleaseURL = release.URL
	status.Available, status.Reason = UpdateAvailable(s.current, release.Tag)
	if !status.Available {
		if status.Reason == ReasonMalformed {
			return Result{}, fmt.Errorf("%w: %s", ErrMalformedRelease, release.Tag)
		}
		return Result{AlreadyCurrent: true, Status: status}, nil
	}
	pair, err := release.ArtifactFor(s.target)
	if err != nil {
		return Result{}, err
	}
	if err := checkDestination(executable); err != nil {
		return Result{}, err
	}
	dir := filepath.Dir(executable)
	if err := checkWritable(dir); err != nil {
		return Result{}, err
	}
	lock, err := acquireLock(s.lockPath, s.now)
	if err != nil {
		return Result{}, err
	}
	defer lock.release()

	if s.progress != nil {
		s.progress(StageDownloading)
	}
	manifest, err := s.download().FetchBytes(ctx, pair.Checksum.URL, maxChecksumBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download checksums: %w", err)
	}
	archivePath, _, err := s.download().DownloadToFile(ctx, pair.Archive.URL, dir)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.Remove(archivePath) }()

	if s.progress != nil {
		s.progress(StageVerifyingArtifact)
	}
	verify := s.verify
	if verify == nil {
		verify = func(_ context.Context, artifactPath, artifactName string, manifest []byte) error {
			return VerifyChecksums(artifactPath, artifactName, manifest)
		}
	}
	if err := verify(ctx, archivePath, pair.Archive.Name, manifest); err != nil {
		return Result{}, err
	}

	if s.progress != nil {
		s.progress(StageExtracting)
	}
	binaryPath, err := ExtractExecutable(archivePath, dir)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.Remove(binaryPath) }()

	if s.progress != nil {
		s.progress(StageInstalling)
	}
	if err := s.installer.Install(ctx, InstallRequest{
		SourcePath:    binaryPath,
		Destination:   executable,
		Version:       release.Tag,
		Mode:          0o755,
		VerifyTimeout: DefaultVerifyTimeout,
		Stage:         s.progress,
	}); err != nil {
		return Result{}, err
	}

	now := s.now().UTC()
	// The install is already verified and durable. Recording the lifecycle
	// transition is therefore best-effort: a machine-state write failure must
	// not report the successful replacement as a failed update. Without the
	// record the next launch simply presents no notice, which is truthful.
	if s.transition != nil {
		transition := NewVersionTransition(OperationUpdate, s.current, release.Tag, now)
		_ = s.transition.Save(transition)
	}
	cache.LatestVersion = release.Tag
	cache.ReleaseID = release.ID
	cache.ReleaseURL = release.URL
	cache.ETag = release.ETag
	cache.CheckedAt = now
	cache.LastAttemptAt = now
	cache.LastError = ""
	s.saveCache(cache)
	return Result{InstalledVersion: release.Tag, Path: executable, Status: status}, nil
}

// resolveLatest fetches the latest release, falling back to the cached body
// when the source answers not-modified. notModified reports that the returned
// release data came from the cache rather than a fresh response.
func (s *Service) resolveLatest(ctx context.Context, conditional bool) (Release, Cache, bool, error) {
	cache, _ := s.loadCache()
	if s.source == nil {
		return Release{}, cache, false, fmt.Errorf("release source is unavailable")
	}
	etag := ""
	if conditional && cache.LatestVersion != "" {
		etag = cache.ETag
	}
	timeout := DefaultMetadataTimeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	release, notModified, err := s.source.Latest(requestCtx, etag)
	if err != nil {
		return Release{}, cache, false, fmt.Errorf("resolve release: %w", err)
	}
	if notModified {
		if cache.LatestVersion == "" {
			return Release{}, cache, false, fmt.Errorf("%w: not modified without a cached release", ErrMalformedRelease)
		}
		parsed, ok := ParseVersion(cache.LatestVersion)
		if !ok {
			return Release{}, cache, false, fmt.Errorf("%w: cached version %q", ErrMalformedRelease, cache.LatestVersion)
		}
		return Release{
			Version:    parsed,
			Tag:        cache.LatestVersion,
			ID:         cache.ReleaseID,
			URL:        cache.ReleaseURL,
			Prerelease: len(parsed.Pre) > 0,
			ETag:       cache.ETag,
		}, cache, true, nil
	}
	return release, cache, false, nil
}

func (s *Service) statusFrom(cache Cache) Status {
	status := Status{
		CurrentVersion:    s.current,
		LatestVersion:     cache.LatestVersion,
		CheckedAt:         cache.CheckedAt,
		Cached:            true,
		ReleaseURL:        cache.ReleaseURL,
		Stale:             !Fresh(cache, s.ttl, s.now()),
		PlatformSupported: s.target.Supported(),
	}
	status.Available, status.Reason = UpdateAvailable(s.current, cache.LatestVersion)
	if cache.LatestVersion == "" {
		status.Reason = ReasonNotChecked
	}
	if !status.PlatformSupported {
		status.Available = false
		status.Reason = ReasonUnsupported
	}
	return status
}

func (s *Service) loadCache() (Cache, bool) {
	if s.cache == nil {
		return Cache{}, false
	}
	return s.cache.Load()
}

func (s *Service) saveCache(cache Cache) {
	if s.cache == nil {
		return
	}
	// The cache is an optimization. A write failure degrades the next launch
	// to "stale", which is safe, so it must never fail a check.
	_ = s.cache.Save(cache)
}

func (s *Service) download() *Downloader {
	if s.downloader == nil {
		s.downloader = &Downloader{}
	}
	return s.downloader
}

func resolveExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func checkDestination(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("update: inspect installed executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("update: installed path %s is not a regular file", path)
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("update: installed executable %s is owned by another user; refusing to replace it", path)
	}
	return nil
}

// checkWritable proves the destination directory accepts a new file before
// any download happens. It still does not prove the final rename will succeed,
// but it turns the common permission failure into an early, clear one.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".ohgo-writecheck-*")
	if err != nil {
		return fmt.Errorf("update: destination directory %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

func machineLockPath() (string, error) {
	return state.Path("update.lock")
}
