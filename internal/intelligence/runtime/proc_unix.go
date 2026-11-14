//go:build unix

package runtime

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the adapter in its own process group so reaping also
// reaches a debuggee the adapter spawned. Without this, killing a hung adapter
// can orphan the process being debugged.
func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcess kills the whole adapter process group.
func killProcess(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	_ = command.Process.Kill()
}
