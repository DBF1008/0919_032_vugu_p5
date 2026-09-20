//go:build !windows

package devutil

import (
	"os/exec"
	"syscall"
)

// configureCommand puts the command into a new process group so that we can
// terminate the whole tree (the build tool plus any compilers it spawns)
// by signalling the group instead of only the parent process.
func configureCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to the process group led by the command.
func killProcessGroup(pid int) {
	// Negative pid targets the whole process group.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
