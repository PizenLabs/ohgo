//go:build linux || darwin

package update

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether replacing info's file would preserve the
// owner. A group-writable directory can otherwise let one user overwrite
// another user's installation, which is not a permission the updater may take
// for itself.
func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Uid == uint32(os.Geteuid())
}
