//go:build windows

package exec

import "os/exec"

// setProcessGroup is a no-op on Windows: there is no POSIX process group to
// join, and exec.CommandContext's default kill is the best available.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup terminates the child process directly.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
