//go:build !windows

package exec

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group so a cancellation
// can signal the whole tree, not just the shell that was launched.
//
// Without this, "sh -c 'sleep 5'" leaves the forked sleep alive when only the
// shell is killed; the orphan keeps the stdout/stderr pipes open and Wait
// blocks until it finishes on its own.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the child's process group, reaping the
// shell and every command it spawned.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// A negative pid targets the whole group created by setProcessGroup.
	// The group may already be gone when cancellation races with exit, which
	// is success, not a failure to report.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
