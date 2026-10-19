//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the command in a process group of its own, so stopping it
// stops what it started too.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// stopGroup asks the command's process group to stop, or kills it.
func stopGroup(cmd *exec.Cmd, kill bool) error {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}
