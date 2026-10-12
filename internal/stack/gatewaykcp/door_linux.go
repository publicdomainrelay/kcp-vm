//go:build linux

package gatewaykcp

import (
	"os/exec"
	"syscall"
)

func dieWithCore(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
