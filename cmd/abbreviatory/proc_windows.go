//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// ownGroup does nothing on Windows; stopGroup stops the process tree instead.
func ownGroup(cmd *exec.Cmd) {}

// stopGroup ends the command and every process it started (taskkill /T),
// or, if that fails, the command alone.
func stopGroup(cmd *exec.Cmd, kill bool) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
