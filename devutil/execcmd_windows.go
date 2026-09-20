//go:build windows

package devutil

import (
	"os/exec"
	"strconv"
	"syscall"
)

// configureCommand creates the command in a new process group (required for
// taskkill /T to be able to terminate the whole process tree).
func configureCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags = syscall.CREATE_NEW_PROCESS_GROUP
}

// killProcessGroup terminates the command and every process it created.
func killProcessGroup(pid int) {
	taskkill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	_ = taskkill.Run()
}
