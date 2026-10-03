package clusterprovider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

const (
	PostgresDataDirName = "postgres"

	PostgresDatabase = "k3s"

	PostgresVaultDatabase = clusterstack.OpenBaoDatabase

	PostgresUser = "postgres"

	postgresLoopbackHost = "127.0.0.1"

	postgresLoopbackCIDR = "127.0.0.1/32"

	postgresPasswordFile = "postgres.pw"

	postgresLogFile = "postgres.log"

	postgresReadyTimeout = 60 * time.Second
)

var ErrNoPostgresBinaries = errors.New(
	"clusterprovider: the per-cluster postgres needs initdb, postgres and psql on this host " +
		"(PostgresSpec.BinDir names a directory, or they are searched on PATH). Without them " +
		"there is no durable store, and the guest would fall back to its own sqlite")

type PostgresSpec struct {
	Cluster string

	StateDir string

	Port int

	BinDir string

	Log *slog.Logger
}

type Postgres struct {
	spec     PostgresSpec
	dataDir  string
	password string

	cmd *exec.Cmd
	log *slog.Logger
}

func (s PostgresSpec) DataDir() string { return filepath.Join(s.StateDir, PostgresDataDirName) }

func (p *Postgres) DSN() string {
	return p.dsnFor(PostgresDatabase)
}

func (p *Postgres) VaultDSN() string {
	return p.dsnFor(PostgresVaultDatabase)
}

func (p *Postgres) dsnFor(database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		PostgresUser, p.password, postgresLoopbackHost, p.spec.Port, database)
}

func (p *Postgres) Up() bool {
	if p.cmd != nil && p.cmd.ProcessState != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(postgresLoopbackHost, strconv.Itoa(p.spec.Port)), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (p *Postgres) DataDir() string { return p.dataDir }

func StartPostgres(ctx context.Context, spec PostgresSpec) (*Postgres, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	log := spec.Log
	if log == nil {
		log = slog.Default()
	}
	initdb, postgres, psql, err := spec.binaries()
	if err != nil {
		return nil, err
	}

	p := &Postgres{spec: spec, dataDir: spec.DataDir(), log: log}

	if err := p.loadOrCreatePassword(); err != nil {
		return nil, err
	}

	if p.serving(ctx, psql) {
		if err := p.ensureVaultDatabase(ctx, psql); err != nil {
			return nil, err
		}
		log.Info("postgres: adopted a running server", "cluster", spec.Cluster,
			"port", spec.Port, "dataDir", p.dataDir)
		return p, nil
	}

	if err := p.clearStalePid(); err != nil {
		return nil, err
	}

	fresh, err := p.ensureInitialised(ctx, initdb)
	if err != nil {
		return nil, err
	}
	if err := p.start(ctx, postgres); err != nil {
		return nil, err
	}
	if err := p.waitServing(ctx, psql); err != nil {
		return nil, err
	}
	if fresh {
		if err := p.createDatabase(ctx, psql); err != nil {
			return nil, err
		}
	}
	if err := p.ensureVaultDatabase(ctx, psql); err != nil {
		return nil, err
	}
	log.Info("postgres: serving", "cluster", spec.Cluster, "port", spec.Port,
		"dataDir", p.dataDir, "initialised", fresh)
	return p, nil
}

func (s PostgresSpec) validate() error {
	switch {
	case s.Cluster == "":
		return errors.New("clusterprovider: PostgresSpec.Cluster is required")
	case s.StateDir == "":
		return errors.New("clusterprovider: PostgresSpec.StateDir is required; it is the store's own root and the one thing a re-provision must find again")
	case s.Port <= 0 || s.Port > 65535:
		return fmt.Errorf("clusterprovider: PostgresSpec.Port is required and was %d: a default here is ADR 0026's byte-identical DSN, and with every store on the loopback address the port is the only thing left separating two clusters' stores on one host", s.Port)
	}
	return nil
}

func (s PostgresSpec) binaries() (initdb, postgres, psql string, err error) {
	for _, name := range []string{"initdb", "postgres", "psql"} {
		path := name
		if s.BinDir != "" {
			path = filepath.Join(s.BinDir, name)
			if _, statErr := os.Stat(path); statErr != nil {
				return "", "", "", fmt.Errorf("%w: %s is not in BinDir %s", ErrNoPostgresBinaries, name, s.BinDir)
			}
		} else if path, err = exec.LookPath(name); err != nil {
			return "", "", "", fmt.Errorf("%w: %s: %v", ErrNoPostgresBinaries, name, err)
		}
		switch name {
		case "initdb":
			initdb = path
		case "postgres":
			postgres = path
		case "psql":
			psql = path
		}
	}
	return initdb, postgres, psql, nil
}

func (p *Postgres) loadOrCreatePassword() error {
	path := filepath.Join(p.spec.StateDir, postgresPasswordFile)
	b, err := os.ReadFile(path)
	if err == nil {
		p.password = strings.TrimSpace(string(b))
		if p.password == "" {
			return fmt.Errorf("clusterprovider: %s is empty; it is this cluster's postgres credential", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clusterprovider: reading %s: %w", path, err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("clusterprovider: generating the postgres password: %w", err)
	}
	p.password = hex.EncodeToString(buf)
	if err := os.MkdirAll(p.spec.StateDir, 0o700); err != nil {
		return fmt.Errorf("clusterprovider: state dir %s: %w", p.spec.StateDir, err)
	}
	if err := os.WriteFile(path, []byte(p.password+"\n"), 0o600); err != nil {
		return fmt.Errorf("clusterprovider: writing %s: %w", path, err)
	}
	return nil
}

func (p *Postgres) serving(ctx context.Context, psql string) bool {
	dir, err := p.query(ctx, psql, "show data_directory")
	if err != nil {
		return false
	}
	return filepath.Clean(dir) == filepath.Clean(p.dataDir)
}

func (p *Postgres) clearStalePid() error {
	path := filepath.Join(p.dataDir, "postmaster.pid")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clusterprovider: reading %s: %w", path, err)
	}
	fields := strings.Fields(string(b))
	if len(fields) > 0 {
		if pid, err := strconv.Atoi(fields[0]); err == nil && pid > 0 && p.pidIsOurPostmaster(pid) {
			return fmt.Errorf(
				"clusterprovider: %s names postmaster pid %d, which is alive over %s, so this "+
					"store is not stale and starting a second postmaster over it would corrupt it",
				path, pid, p.dataDir)
		}
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("clusterprovider: removing the stale %s: %w", path, err)
	}
	return nil
}

func (p *Postgres) pidIsOurPostmaster(pid int) bool {
	argv, err := hostos.Cmdline(pid)
	if err != nil {
		return false
	}
	for _, arg := range argv {
		if strings.Contains(arg, p.dataDir) {
			return true
		}
	}
	return false
}

func (p *Postgres) ensureInitialised(ctx context.Context, initdb string) (bool, error) {
	if _, err := os.Stat(filepath.Join(p.dataDir, "PG_VERSION")); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(p.spec.StateDir, 0o700); err != nil {
		return false, fmt.Errorf("clusterprovider: state dir %s: %w", p.spec.StateDir, err)
	}
	pwFile, err := os.CreateTemp(p.spec.StateDir, ".pwfile-*")
	if err != nil {
		return false, fmt.Errorf("clusterprovider: password file: %w", err)
	}
	defer os.Remove(pwFile.Name())
	if _, err := pwFile.WriteString(p.password); err != nil {
		pwFile.Close()
		return false, fmt.Errorf("clusterprovider: password file: %w", err)
	}
	if err := pwFile.Close(); err != nil {
		return false, fmt.Errorf("clusterprovider: password file: %w", err)
	}

	argv := []string{
		"-D", p.dataDir,
		"-U", PostgresUser,
		"--pwfile=" + pwFile.Name(),
		"--auth-host=scram-sha-256",
		"--encoding=UTF8",
	}
	if out, err := exec.CommandContext(ctx, initdb, argv...).CombinedOutput(); err != nil {
		return false, fmt.Errorf("clusterprovider: initdb -D %s: %w: %s", p.dataDir, err, out)
	}

	hba := filepath.Join(p.dataDir, "pg_hba.conf")
	f, err := os.OpenFile(hba, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("clusterprovider: opening %s: %w", hba, err)
	}
	line := fmt.Sprintf("\nhost all all %s scram-sha-256\n", postgresLoopbackCIDR)
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return false, fmt.Errorf("clusterprovider: appending to %s: %w", hba, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("clusterprovider: closing %s: %w", hba, err)
	}
	return true, nil
}

func (p *Postgres) start(ctx context.Context, postgres string) error {
	logPath := filepath.Join(p.spec.StateDir, postgresLogFile)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("clusterprovider: postgres log %s: %w", logPath, err)
	}
	argv := []string{
		"-D", p.dataDir,
		"-p", strconv.Itoa(p.spec.Port),
		"-c", "listen_addresses=" + postgresLoopbackHost,
		"-c", "unix_socket_directories=",
	}
	cmd := exec.Command(postgres, argv...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("clusterprovider: starting postgres on port %d: %w", p.spec.Port, err)
	}
	go func() { _ = cmd.Wait() }()
	p.cmd = cmd
	p.log.Info("postgres: started", "cluster", p.spec.Cluster, "port", p.spec.Port,
		"pid", cmd.Process.Pid, "log", logPath)
	return nil
}

func (p *Postgres) waitServing(ctx context.Context, psql string) error {
	logPath := filepath.Join(p.spec.StateDir, postgresLogFile)
	deadline := time.Now().Add(postgresReadyTimeout)
	for {
		if p.serving(ctx, psql) {
			return nil
		}
		if p.cmd != nil && p.cmd.ProcessState != nil {
			return fmt.Errorf("clusterprovider: postgres on port %d exited with %v during startup; see %s",
				p.spec.Port, p.cmd.ProcessState, logPath)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("clusterprovider: no postgres serving %s on port %d after %s; see %s",
				p.dataDir, p.spec.Port, postgresReadyTimeout, logPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (p *Postgres) query(ctx context.Context, psql, stmt string) (string, error) {
	argv := []string{
		"-h", postgresLoopbackHost,
		"-p", strconv.Itoa(p.spec.Port),
		"-U", PostgresUser,
		"-d", "postgres",
		"-tAc", stmt,
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, psql, argv...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+p.password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("psql %s: %w: %s", stmt, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Postgres) createDatabase(ctx context.Context, psql string) error {
	if _, err := p.query(ctx, psql, `CREATE DATABASE "`+PostgresDatabase+`"`); err != nil {
		return fmt.Errorf("clusterprovider: creating database %s: %w", PostgresDatabase, err)
	}
	return nil
}

func (p *Postgres) ensureVaultDatabase(ctx context.Context, psql string) error {
	exists, err := p.query(ctx, psql,
		`SELECT 1 FROM pg_database WHERE datname='`+PostgresVaultDatabase+`'`)
	if err != nil {
		return fmt.Errorf("clusterprovider: checking for the vault database %s: %w",
			PostgresVaultDatabase, err)
	}
	if exists == "1" {
		return nil
	}
	if _, err := p.query(ctx, psql, `CREATE DATABASE "`+PostgresVaultDatabase+`"`); err != nil {
		return fmt.Errorf("clusterprovider: creating the vault database %s: %w",
			PostgresVaultDatabase, err)
	}
	return nil
}

func (p *Postgres) Stop(ctx context.Context) error {
	pidPath := filepath.Join(p.dataDir, "postmaster.pid")
	b, err := os.ReadFile(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clusterprovider: reading %s: %w", pidPath, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]))
	if err != nil {
		return fmt.Errorf("clusterprovider: %s: %w", pidPath, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("clusterprovider: pid %d from %s: %w", pid, pidPath, err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("clusterprovider: signalling postgres pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(pidPath); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("clusterprovider: postgres pid %d still holds %s after 30s", pid, pidPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
