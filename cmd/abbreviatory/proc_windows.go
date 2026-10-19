//go:build windows

package main

import "os/exec"

func ownGroup(cmd *exec.Cmd) {}

func stopGroup(cmd *exec.Cmd, kill bool) error { return cmd.Process.Kill() }
