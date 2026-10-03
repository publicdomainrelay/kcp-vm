//go:build windows

package hostos

import (
	"os"

	"golang.org/x/sys/windows"
)

const (
	LockShared = 1

	LockExclusive = 2

	LockNonBlocking = 4
)

func Lock(f *os.File, how int) error {
	flags := uint32(0)
	if how&LockExclusive != 0 {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if how&LockNonBlocking != 0 {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlapped)
}

func Unlock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
