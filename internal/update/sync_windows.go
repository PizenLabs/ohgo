//go:build windows

package update

// syncDir is a no-op on Windows: directories cannot be opened for a durability
// flush in the Unix sense. Windows installation is not part of the published
// release matrix, so this path exists only so the package builds there and can
// return a truthful unsupported-platform result.
func syncDir(string) error { return nil }
