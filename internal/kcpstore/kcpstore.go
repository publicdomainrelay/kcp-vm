package kcpstore

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const KineStopTimeout = 10 * time.Second

type KineConfig struct {
	Unit  string
	Slice string
	Exec  string
	Dir   string
}

func KineSqliteArgv(c KineConfig) ([]string, error) {
	if !filepath.IsAbs(c.Exec) {
		return nil, fmt.Errorf("kcpstore: the kine executable must be an absolute path, got %q (dev builds take it from kine-path, never PATH)", c.Exec)
	}
	if !filepath.IsAbs(c.Dir) {
		return nil, errors.New("kcpstore: the kine directory must be absolute")
	}
	return []string{c.Exec,
		"--endpoint", "sqlite://" + filepath.Join(c.Dir, "kine.db"),
		"--listen-address", "127.0.0.1:0",
		"--metrics-bind-address=0",
	}, nil
}

func KineSqliteSpec(c KineConfig) (hostsvc.UnitSpec, error) {
	argv, err := KineSqliteArgv(c)
	if err != nil {
		return hostsvc.UnitSpec{}, err
	}
	return hostsvc.UnitSpec{
		Name:        c.Unit,
		Description: "kine on sqlite for one application instance's kcp",
		Exec:        argv,
		WorkDir:     c.Dir,
		Slice:       c.Slice,
		Restart:     hostsvc.RestartOnFailure,
		KillSignal:  syscall.SIGTERM,
		StopTimeout: KineStopTimeout,
	}, nil
}
