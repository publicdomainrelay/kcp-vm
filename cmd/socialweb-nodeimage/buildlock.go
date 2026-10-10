package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const stageLockSuffix = ".lock"

func withStageDirHeld(cfg Config, build func() error) error {
	lockPath := cfg.StageDir + stageLockSuffix
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s, where the stage directory's lock lives: "+
			"%w -- a build that cannot say whether another one holds the stage directory is a build "+
			"that writes into a tree another build is writing into", filepath.Dir(lockPath), err)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: opening the stage directory's lock at %s: %w",
			lockPath, err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("socialweb-nodeimage: another build is holding the stage directory %s, "+
				"so this one is not starting. A build unsquashes the base image into that directory "+
				"and chroots into it in place, so two builds sharing it interleave their writes into "+
				"one tree and the image published from it is neither build's -- and nothing downstream "+
				"would report that, because both builds finish and both publish. It refuses rather "+
				"than waiting because a build takes minutes and a caller waiting on one cannot tell a "+
				"queue from a hang. %s",
				cfg.StageDir, describeStageLockHolder(lock, lockPath))
		}
		return fmt.Errorf("socialweb-nodeimage: taking the stage directory's lock at %s: %w",
			lockPath, err)
	}
	defer func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	}()
	recordStageLockHolder(lock)
	return build()
}

func recordStageLockHolder(lock *os.File) {
	if err := lock.Truncate(0); err != nil {
		return
	}
	if _, err := lock.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return
	}
}

func describeStageLockHolder(lock *os.File, lockPath string) string {
	if _, err := lock.Seek(0, io.SeekStart); err != nil {
		return fmt.Sprintf("The lock is %s.", lockPath)
	}
	raw, err := io.ReadAll(io.LimitReader(lock, 64))
	if err != nil {
		return fmt.Sprintf("The lock is %s.", lockPath)
	}
	holder := strings.TrimSpace(string(raw))
	if holder == "" {
		return fmt.Sprintf("The lock is %s.", lockPath)
	}
	return fmt.Sprintf("The lock is %s, which reads pid %s.", lockPath, holder)
}
