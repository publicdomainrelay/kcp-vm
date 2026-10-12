package kcpstore

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const (
	KineStopTimeout     = 10 * time.Second
	PostgresStopTimeout = 30 * time.Second
	MaxSocketPath       = 107
	PostgresSocketName  = ".s.PGSQL.5432"
	PostgresUser        = "kcp"
	KineDatabase        = "kine"
)

type KineConfig struct {
	Unit              string
	Slice             string
	Exec              string
	Dir               string
	Socket            string
	PostgresSocketDir string
}

type PostgresConfig struct {
	Unit      string
	Slice     string
	BinDir    string
	Data      string
	SocketDir string
}

func checkSocket(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("kcpstore: socket path %q must be absolute", path)
	}
	if len(path) > MaxSocketPath {
		return fmt.Errorf("kcpstore: socket path %s is %d bytes, over the %d-byte sun_path limit", path, len(path), MaxSocketPath)
	}
	return nil
}

func kineArgv(c KineConfig, endpoint string) ([]string, error) {
	if !filepath.IsAbs(c.Exec) {
		return nil, fmt.Errorf("kcpstore: the kine executable must be an absolute path, got %q (dev builds take it from kine-path, never PATH)", c.Exec)
	}
	if !filepath.IsAbs(c.Dir) {
		return nil, errors.New("kcpstore: the kine directory must be absolute")
	}
	if err := checkSocket(c.Socket); err != nil {
		return nil, err
	}
	return []string{c.Exec,
		"--endpoint", endpoint,
		"--listen-address", "unix://" + c.Socket,
		"--metrics-bind-address=0",
	}, nil
}

func KineSqliteArgv(c KineConfig) ([]string, error) {
	return kineArgv(c, "sqlite://"+filepath.Join(c.Dir, SqliteFile))
}

func PostgresDSN(socketDir string) string {
	return "postgres://" + PostgresUser + "@localhost/" + KineDatabase + "?host=" + socketDir + "&sslmode=disable"
}

func KinePostgresArgv(c KineConfig) ([]string, error) {
	if err := checkSocket(filepath.Join(c.PostgresSocketDir, PostgresSocketName)); err != nil {
		return nil, err
	}
	return kineArgv(c, PostgresDSN(c.PostgresSocketDir))
}

func kineSpec(c KineConfig, argv []string, desc string) hostsvc.UnitSpec {
	return hostsvc.UnitSpec{
		Name:        c.Unit,
		Description: desc,
		Exec:        argv,
		WorkDir:     c.Dir,
		Slice:       c.Slice,
		Restart:     hostsvc.RestartOnFailure,
		KillSignal:  syscall.SIGTERM,
		StopTimeout: KineStopTimeout,
	}
}

func KineSqliteSpec(c KineConfig) (hostsvc.UnitSpec, error) {
	argv, err := KineSqliteArgv(c)
	if err != nil {
		return hostsvc.UnitSpec{}, err
	}
	return kineSpec(c, argv, "kine on sqlite for one application instance's kcp"), nil
}

func KinePostgresSpec(c KineConfig, store string) (hostsvc.UnitSpec, error) {
	argv, err := KinePostgresArgv(c)
	if err != nil {
		return hostsvc.UnitSpec{}, err
	}
	spec := kineSpec(c, argv, "kine on the application instance's postgres for one kcp slot")
	spec.After, spec.Wants = []string{store}, []string{store}
	return spec, nil
}

func PostgresSpec(c PostgresConfig) (hostsvc.UnitSpec, error) {
	if !filepath.IsAbs(c.BinDir) || !filepath.IsAbs(c.Data) {
		return hostsvc.UnitSpec{}, fmt.Errorf("kcpstore: the postgres bin dir and data dir must be absolute paths (got %q, %q)", c.BinDir, c.Data)
	}
	if err := checkSocket(filepath.Join(c.SocketDir, PostgresSocketName)); err != nil {
		return hostsvc.UnitSpec{}, err
	}
	return hostsvc.UnitSpec{
		Name:        c.Unit,
		Description: "postgres holding one application instance's kcp store (unix socket only)",
		Exec: []string{filepath.Join(c.BinDir, "postgres"), "-D", c.Data,
			"-c", "listen_addresses=", "-c", "unix_socket_directories=" + c.SocketDir},
		WorkDir:     c.Data,
		Slice:       c.Slice,
		Restart:     hostsvc.RestartOnFailure,
		KillSignal:  syscall.SIGINT,
		StopTimeout: PostgresStopTimeout,
	}, nil
}

func InitdbArgv(binDir, data string) []string {
	return []string{filepath.Join(binDir, "initdb"), "-D", data, "-U", PostgresUser, "--auth=trust", "-E", "UTF8"}
}

func ReadyArgv(binDir, socketDir string) []string {
	return []string{filepath.Join(binDir, "psql"), "-h", socketDir, "-U", PostgresUser, "-d", "postgres", "-Atc", "select 1"}
}
