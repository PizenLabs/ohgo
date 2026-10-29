package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrInstallVerification means the replacement was written but could not be
// confirmed as the requested ohgo release. The installer restores the previous
// binary before returning it.
var ErrInstallVerification = errors.New("update: installed binary failed verification")

// Installer replaces one executable atomically and keeps the previous binary
// until the replacement is verified.
//
// It is a value with injected seams rather than a global service so a test can
// exercise the replace/verify/rollback sequence against a temporary directory
// without touching the running development binary.
type Installer struct {
	// VersionOf reports the version an installed binary prints. Nil uses the
	// production `ohgo version` probe.
	VersionOf func(ctx context.Context, path string) (string, error)
	// Now supplies the clock for stale-temporary cleanup. Nil uses time.Now.
	Now func() time.Time
}

// InstallRequest is one verified replacement.
type InstallRequest struct {
	// SourcePath is the extracted, checksum-verified temporary binary.
	SourcePath string
	// Destination is the resolved installed executable.
	Destination string
	// Version is the release version the destination must report.
	Version string
	// Mode is the final permission bits, normally 0755.
	Mode os.FileMode
	// VerifyTimeout bounds the post-install version probe.
	VerifyTimeout time.Duration
	// Stage receives the installation sub-stage. Nil is silent.
	Stage func(Stage)
}

// Install performs the replacement transaction.
//
// The destination path is never removed before a verified replacement exists:
// the previous binary is copied to a sibling backup, the new binary is renamed
// over the destination in one filesystem operation, and the backup is only
// deleted after the new binary answers `version` with the requested release.
func (i *Installer) Install(ctx context.Context, req InstallRequest) (err error) {
	if strings.TrimSpace(req.SourcePath) == "" || strings.TrimSpace(req.Destination) == "" {
		return fmt.Errorf("install: source and destination are required")
	}
	sourceInfo, err := os.Stat(req.SourcePath)
	if err != nil {
		return fmt.Errorf("install: inspect downloaded binary: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() || sourceInfo.Size() == 0 {
		return fmt.Errorf("install: downloaded binary is not a non-empty regular file")
	}
	destInfo, err := os.Lstat(req.Destination)
	if err != nil {
		return fmt.Errorf("install: inspect destination: %w", err)
	}
	if !destInfo.Mode().IsRegular() {
		return fmt.Errorf("install: destination %s is not a regular file", req.Destination)
	}
	if !ownedByCurrentUser(destInfo) {
		return fmt.Errorf("install: destination %s is owned by another user", req.Destination)
	}
	dir := filepath.Dir(req.Destination)
	mode := req.Mode
	if mode == 0 {
		mode = 0o755
	}
	backup := backupPath(req.Destination)
	now := i.Now
	if now == nil {
		now = time.Now
	}
	// A killed updater cannot run its deferred cleanup. Remove only our own
	// stale temporaries after a generous age, never a fresh file another
	// process may be using and never the deterministic backup.
	cleanupStaleTemps(dir, now())

	// Copying rather than renaming the current binary keeps the destination
	// usable during the whole transaction: an interruption before the final
	// rename leaves the old installation in place.
	if err := copyFile(req.Destination, backup, destInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("install: preserve previous binary: %w", err)
	}
	backupReady := true
	defer func() {
		if err != nil && backupReady {
			_ = os.Remove(backup)
		}
	}()

	if err := os.Chmod(req.SourcePath, mode); err != nil {
		return fmt.Errorf("install: set executable permissions: %w", err)
	}
	if err := syncFile(req.SourcePath); err != nil {
		return fmt.Errorf("install: sync downloaded binary: %w", err)
	}
	if err := os.Rename(req.SourcePath, req.Destination); err != nil {
		return fmt.Errorf("install: replace executable: %w", err)
	}
	// From here the new binary is at the destination. The backup must survive
	// every failure path below.
	backupReady = false
	if req.Stage != nil {
		req.Stage(StageVerifyingInstallation)
	}
	if err := syncDir(dir); err != nil {
		// Durability is best effort on filesystems that do not support a
		// directory sync. The replacement is still atomic and verifiable.
		_ = err
	}
	if verr := i.verify(ctx, req); verr != nil {
		restoreErr := os.Rename(backup, req.Destination)
		if syncErr := syncDir(dir); syncErr != nil {
			_ = syncErr
		}
		if restoreErr != nil {
			return fmt.Errorf("%w: %v; previous binary preserved at %s: restore failed: %v",
				ErrInstallVerification, verr, backup, restoreErr)
		}
		return fmt.Errorf("%w: %v; previous binary restored", ErrInstallVerification, verr)
	}
	_ = os.Remove(backup)
	_ = syncDir(dir)
	return nil
}

func backupPath(destination string) string {
	return filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".ohgo-prev")
}

func (i *Installer) verify(ctx context.Context, req InstallRequest) error {
	info, err := os.Stat(req.Destination)
	if err != nil {
		return fmt.Errorf("stat installed binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("installed path is not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("installed path is not executable")
	}
	want, ok := ParseVersion(req.Version)
	if !ok {
		return fmt.Errorf("requested version %q is not a release", req.Version)
	}
	timeout := req.VerifyTimeout
	if timeout <= 0 {
		timeout = DefaultVerifyTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	versionOf := i.VersionOf
	if versionOf == nil {
		versionOf = InstalledVersion
	}
	gotRaw, err := versionOf(probeCtx, req.Destination)
	if err != nil {
		return fmt.Errorf("probe installed binary: %w", err)
	}
	got, ok := ParseVersion(gotRaw)
	if !ok {
		return fmt.Errorf("installed binary reported %q, which is not a release", gotRaw)
	}
	if !got.Equal(want) {
		return fmt.Errorf("installed binary reports %s, expected %s", got.Display(), want.Display())
	}
	return nil
}

// InstalledVersion probes an ohgo binary with its `version` subcommand.
//
// Only the already-verified local binary is executed. The command is structured
// (no shell), bounded by the caller's context, and its output is parsed rather
// than trusted as a message.
func InstalledVersion(ctx context.Context, path string) (string, error) {
	command := exec.CommandContext(ctx, path, "version")
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("ohgo version exited: %s", exit.String())
		}
		return "", err
	}
	line := strings.TrimSpace(string(output))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if !strings.HasPrefix(line, "ohgo ") {
		return "", fmt.Errorf("unexpected version output")
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "ohgo ")), nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// The destination is a sibling temp first so a crash cannot leave a
	// half-written backup where the installer expects a complete one.
	dir := filepath.Dir(destination)
	tmp, err := os.CreateTemp(dir, ".ohgo-prev-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.ReadFrom(in); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// Replacing an older interrupted backup with the current, still-running
	// binary is safe: the current binary is by definition usable.
	if err := os.Rename(tmpName, destination); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}

// staleTempAge is how long an abandoned download or backup temp file may
// remain. It is far longer than any bounded update, so a live transaction is
// never touched.
const staleTempAge = 24 * time.Hour

func cleanupStaleTemps(dir string, now time.Time) {
	for _, pattern := range []string{".ohgo-update-*", ".ohgo-prev-*"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			continue
		}
		for _, match := range matches {
			info, statErr := os.Lstat(match)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			if now.Sub(info.ModTime()) > staleTempAge {
				_ = os.Remove(match)
			}
		}
	}
}
