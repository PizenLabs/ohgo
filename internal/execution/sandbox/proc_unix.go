//go:build !windows

package sandbox

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group so a
// cancellation can signal the whole tree, not just the process the runtime
// launched.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the child's process group. A group that is
// already gone is success: cancellation races with a natural exit.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
