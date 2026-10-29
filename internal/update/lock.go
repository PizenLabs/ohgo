package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// installLock is one exclusive installation claim.
//
// On the published platforms the claim is an OS advisory lock, so a process
// that dies releases it automatically and no stale timeout is needed. The file
// itself is a diagnostic breadcrumb and is removed by its owner.
type installLock struct {
	file *os.File
	path string
}

// acquireLock takes an exclusive installation claim.
//
// The lock is non-blocking: a second updater is refused rather than queued,
// because the first one is already doing the work.
func acquireLock(path string, now func() time.Time) (*installLock, error) {
	if strings.TrimSpace(path) == "" {
		// Without a lock path the safest fallback is to refuse rather than
		// run two installers with no coordination.
		return nil, fmt.Errorf("%w: no lock path", ErrLockUnavailable)
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("update: create lock directory: %w", err)
	}
	file, err := openLockFile(path)
	if err != nil {
		return nil, fmt.Errorf("update: open lock: %w", err)
	}
	if err := tryLock(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errLockHeld) {
			return nil, ErrUpdateBusy
		}
		return nil, fmt.Errorf("update: lock: %w", err)
	}
	// The breadcrumb is diagnostic only; the flock is the authority. A write
	// failure therefore cannot weaken the exclusion.
	_ = file.Truncate(0)
	_, _ = io.WriteString(file, strconv.Itoa(os.Getpid())+"\n"+now().UTC().Format(time.RFC3339)+"\n")
	return &installLock{file: file, path: path}, nil
}

// release drops the claim. Closing the descriptor is what releases the OS
// lock; removing the path is cleanup, and a missing file is already released.
func (l *installLock) release() {
	if l == nil {
		return
	}
	// Unlink while still holding the advisory lock, then close to release it.
	// Removing after the close would let another process lock the same inode
	// between the two steps while we unlinked its path.
	if l.path != "" {
		_ = os.Remove(l.path)
	}
	if l.file != nil {
		_ = l.file.Close()
	}
}
