//go:build !darwin && !linux

package sandbox

// readOnlySupported is false where the runtime has no enforceable filesystem
// boundary. The caller refuses the command; it never runs it unguarded.
func readOnlySupported() bool { return false }

// readOnlyArgv refuses rather than approximating a boundary.
func readOnlyArgv(string, string, []string) ([]string, error) {
	return nil, ErrUnsupported
}
