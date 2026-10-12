//go:build windows

package hostos

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// ponytail: Windows has no Pdeathsig, so a member here outlives a killed provider;
// a job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE is the upgrade.
func DieWithParent() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func NewProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func GracefulSignal() os.Signal { return os.Kill }

func ForcedSignal() os.Signal { return os.Kill }
