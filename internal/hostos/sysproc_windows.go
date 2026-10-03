//go:build windows

package hostos

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func DetachProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}

func NewProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func GracefulSignal() os.Signal { return os.Kill }

func ForcedSignal() os.Signal { return os.Kill }
