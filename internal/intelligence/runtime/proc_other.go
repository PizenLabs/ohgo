//go:build !unix

package runtime

import "os/exec"

// configureProcess is a no-op on platforms without process groups.
func configureProcess(command *exec.Cmd) {}

// killProcess kills the adapter process.
func killProcess(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
