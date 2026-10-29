//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
)

// readOnlySupported reports whether bubblewrap is available. There is no
// portable unprivileged alternative on Linux, so a missing bwrap means read-only
// execution is refused rather than approximated.
func readOnlySupported() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}

// readOnlyArgv builds a bubblewrap invocation that mounts the filesystem
// read-only, re-binds a few caches writable, and then re-protects the workspace.
//
// The order matters: the workspace bind is last, so a writable cache or /tmp
// bind can never re-expose the protected tree. bwrap needs unprivileged user
// namespaces; when the host disables them the command fails and no mutation
// happens.
func readOnlyArgv(root, dir string, argv []string) ([]string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, ErrUnsupported
	}

	args := []string{
		bwrap,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
	}
	for _, writable := range writableDirs() {
		args = append(args, "--bind", writable, writable)
	}
	// Last bind wins. This is the line that protects the workspace even when
	// it lives under /tmp or a cache directory.
	args = append(args, "--ro-bind", root, root)

	workDir := dir
	if workDir == "" {
		workDir = root
	}
	args = append(args, "--chdir", workDir, "--")
	return append(args, argv...), nil
}

// writableDirs lists host paths a read-only command may still need to write:
// temporary space and language/build caches. They are outside the repository by
// construction; if one happens to sit inside it, the final read-only bind of the
// root takes precedence.
func writableDirs() []string {
	candidates := []string{"/tmp"}
	if cache, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, cache)
	}
	for _, env := range []string{"GOCACHE", "GOMODCACHE"} {
		if value := os.Getenv(env); filepath.IsAbs(value) {
			candidates = append(candidates, value)
		}
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	return out
}
