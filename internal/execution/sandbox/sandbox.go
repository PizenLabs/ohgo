// Package sandbox launches child processes behind an enforceable filesystem
// boundary.
//
// It exists because the runtime cannot decide whether an arbitrary command
// mutates the workspace by reading its text: a shell can write through a
// redirect, an interpreter, a build tool, or a program the classifier has never
// seen. The authority decision therefore carries a read-only bit, and this
// package turns that bit into an OS boundary instead of a guess.
//
// The boundary is deliberately small. On darwin it is sandbox-exec; on Linux it
// is bubblewrap; on every other platform a read-only command is refused rather
// than run unguarded. A missing sandbox is a closed door, not a warning.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnsupported means the platform has no enforceable read-only execution
// boundary. Callers must refuse the command rather than running it.
var ErrUnsupported = errors.New("sandbox: read-only execution is unavailable on this platform")

// ErrNoRoot means a read-only command was requested without a workspace root to
// protect.
var ErrNoRoot = errors.New("sandbox: read-only execution requires a workspace root")

// cancelGrace bounds how long a killed process may keep its output pipes open.
// It matches the shell escape's own grace period so both execution paths bound
// cancellation the same way.
const cancelGrace = time.Second

// Spec describes one child process.
type Spec struct {
	// Argv is the program and its arguments.
	Argv []string
	// Dir is the child's working directory.
	Dir string
	// Root is the workspace the read-only boundary protects. It is ignored
	// when ReadOnly is false.
	Root string
	// ReadOnly requires that the child cannot write anywhere under Root.
	ReadOnly bool
}

// Command builds a child process, wrapped in the platform's read-only boundary
// when ReadOnly is set.
//
// A read-only request never falls back to an unguarded command. When the
// platform has no usable boundary, Command returns ErrUnsupported and the
// caller must surface a refusal.
func Command(ctx context.Context, spec Spec) (*exec.Cmd, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("sandbox: empty argv")
	}
	argv := spec.Argv
	if spec.ReadOnly {
		root, err := canonicalRoot(spec.Root)
		if err != nil {
			return nil, err
		}
		argv, err = readOnlyArgv(root, spec.Dir, argv)
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	return cmd, nil
}

// Configure puts a command in its own process group and installs a cancellation
// that kills the whole tree.
//
// Without this, a cancelled shell can leave its descendants running and holding
// the output pipes open, so the call outlives the cancellation that was meant to
// stop it.
func Configure(cmd *exec.Cmd) {
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return Kill(cmd) }
	cmd.WaitDelay = cancelGrace
}

// Kill terminates a command's process group. It is safe when the process has
// already exited.
func Kill(cmd *exec.Cmd) error {
	return killProcessGroup(cmd)
}

// ReadOnlySupported reports whether this platform can enforce a read-only
// workspace boundary.
func ReadOnlySupported() bool {
	return readOnlySupported()
}

// canonicalRoot resolves a workspace root to the path the OS will compare
// against. A root-level symlink would otherwise let the profile protect one
// spelling while the command writes through another.
func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", ErrNoRoot
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve root %q: %w", root, err)
	}
	return resolved, nil
}
