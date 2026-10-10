//go:build linux

package hostos

import (
	"os"
	"syscall"
)

func DetachProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func DieWithParent() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func NewProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func GracefulSignal() os.Signal { return syscall.SIGTERM }

func ForcedSignal() os.Signal { return syscall.SIGKILL }
