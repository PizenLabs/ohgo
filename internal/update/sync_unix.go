//go:build !windows

package update

import (
	"fmt"
	"os"
)

// syncDir flushes a directory entry change. It gives the atomic rename
// durability across a crash on filesystems that support it.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("update: open directory for sync: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("update: sync directory: %w", err)
	}
	return nil
}
