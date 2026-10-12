//go:build unix

package hostos

import (
	"os"
	"syscall"
)

const (
	LockShared = syscall.LOCK_SH

	LockExclusive = syscall.LOCK_EX

	LockNonBlocking = syscall.LOCK_NB
)

func Lock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err != syscall.EINTR {
			return err
		}
	}
}

func Unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
