//go:build !linux

package gatewaykcp

import "os/exec"

func dieWithCore(cmd *exec.Cmd) {}
