//go:build windows

package sandbox

import "os/exec"

// configureProcessGroup is a no-op on Windows: there is no POSIX process group
// to create.
func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup terminates the child process directly.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
