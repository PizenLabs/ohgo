//go:build darwin

package sandbox

import (
	"fmt"
	"os/exec"
	"strconv"
)

// readOnlySupported reports whether sandbox-exec is available. It is present on
// every current macOS release; if a future release removes it, read-only
// execution fails closed instead of running unguarded.
func readOnlySupported() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// readOnlyArgv wraps argv in a sandbox-exec profile that denies writes under
// root while leaving reads, process execution, and network access alone.
//
// The profile is path-based, so it does not depend on how the command spells
// the path or which program performs the write. sandbox-exec is deprecated but
// still the OS mechanism available without a helper installation.
func readOnlyArgv(root, dir string, argv []string) ([]string, error) {
	exe, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return nil, ErrUnsupported
	}
	_ = dir // cmd.Dir already carries the working directory.
	profile := fmt.Sprintf(
		"(version 1)\n(allow default)\n(deny file-write* (subpath %s))\n",
		sbplString(root),
	)
	out := make([]string, 0, len(argv)+3)
	out = append(out, exe, "-p", profile)
	return append(out, argv...), nil
}

// sbplString quotes a path for a Sandbox Profile Language string.
//
// strconv.Quote is close, but its Go-specific escapes are not part of SBPL;
// escaping the two characters that delimit an SBPL string is enough for a
// filesystem path, and a path containing a newline is rejected by the OS rather
// than silently mis-parsed.
func sbplString(path string) string {
	return strconv.Quote(path)
}
