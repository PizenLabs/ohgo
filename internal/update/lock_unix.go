//go:build linux || darwin

package update

import (
	"errors"
	"os"
	"syscall"
)

// errLockHeld is the platform-neutral signal that another process owns the
// claim. It is translated to ErrUpdateBusy at the boundary.
var errLockHeld = errors.New("update: lock held")

// tryLock takes an exclusive advisory lock without blocking.
//
// The kernel releases it when the descriptor closes, including on an abrupt
// process exit, so a killed updater cannot leave a permanent stale claim.
func tryLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errLockHeld
	}
	return err
}

// openLockFile refuses to follow a symlink at the lock path. The lock file is
// truncated while open, so following a planted link would write to whatever it
// names.
func openLockFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
