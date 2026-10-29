//go:build !linux && !darwin

package update

import (
	"errors"
	"os"
)

// errLockHeld is the platform-neutral signal that another process owns the
// claim. It is translated to ErrUpdateBusy at the boundary.
var errLockHeld = errors.New("update: lock held")

// tryLock is a no-op on platforms outside the published release matrix.
// Self-update returns ErrUnsupportedPlatform before it ever reaches the
// installer there, so this fallback exists only to keep the package buildable.
func tryLock(*os.File) error { return nil }

func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}
