package update

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestInstallLockRequiresAPath(t *testing.T) {
	if _, err := acquireLock("", nil); !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("acquireLock with no path = %v, want ErrLockUnavailable", err)
	}
}

func TestInstallLockDoesNotFollowASymlink(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("O_NOFOLLOW lock path is Unix-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("do not truncate"), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	link := filepath.Join(dir, "update.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := acquireLock(link, nil); err == nil {
		t.Fatal("acquireLock followed a symlink")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "do not truncate" {
		t.Fatalf("symlink target was modified: %q, %v", body, err)
	}
}

func TestInstallLockIsExclusiveAndReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	lock, err := acquireLock(path, clock)
	if err != nil {
		t.Fatalf("first acquireLock: %v", err)
	}
	if _, err := acquireLock(path, clock); !errors.Is(err, ErrUpdateBusy) {
		t.Fatalf("second acquireLock = %v, want ErrUpdateBusy", err)
	}
	lock.release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("release left the lock behind: %v", err)
	}
	again, err := acquireLock(path, clock)
	if err != nil {
		t.Fatalf("acquireLock after release: %v", err)
	}
	again.release()
}
