//go:build windows

package devutil

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup places the command in a new process group object
// (CREATE_NEW_PROCESS_GROUP) so it can be killed together with its children.
func configureProcessGroup(cmd *exec.Cmd) {
	attrs := cmd.SysProcAttr
	if attrs == nil {
		attrs = &syscall.SysProcAttr{}
	} else {
		copy := *attrs
		attrs = &copy
	}
	attrs.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr = attrs
}

// killProcessGroup forcibly terminates the command process. Job-object based
// tree killing would additionally reap grandchildren, but this matches the
// best-effort behavior available without extra native dependencies.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
