//go:build !windows

package devutil

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the command in its own process group so that
// a negative-pgid kill terminates the command and all of its children.
func configureProcessGroup(cmd *exec.Cmd) {
	attrs := cmd.SysProcAttr
	if attrs == nil {
		attrs = &syscall.SysProcAttr{}
	} else {
		copy := *attrs
		attrs = &copy
	}
	attrs.Setpgid = true
	cmd.SysProcAttr = attrs
}

// killProcessGroup sends SIGKILL to the whole process group of cmd.
// Negative Pid means "every process in the group led by this process".
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
