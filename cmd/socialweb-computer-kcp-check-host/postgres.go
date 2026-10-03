package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
)

const postgresProbeTimeout = 90 * time.Second

func postgresResult(f facts) Result {
	var binaries []string
	var missing []string
	for _, name := range []string{"initdb", "postgres", "psql"} {
		path := name
		if f.postgresBinDir != "" {
			path = filepath.Join(f.postgresBinDir, name)
			if _, err := os.Stat(path); err != nil {
				missing = append(missing, fmt.Sprintf("%s is not in %s: %v", name, f.postgresBinDir, err))
				continue
			}
		}
		found, err := exec.LookPath(path)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		binaries = append(binaries, found)
	}
	if len(missing) > 0 {
		return Result{
			Label: "host.postgres",
			Kind:  Missing,
			Detail: fmt.Sprintf("the per-cluster store is a real postgres and this host does not "+
				"have one: %s. The store is durable on purpose -- a cluster's objects outlive the "+
				"process group that serves them -- and the reconciler reads the bootstrap row back "+
				"from it on every start, so without it there is no cluster state to come back to. "+
				"Install the server here, or name a BinDir in %s. Nothing here is privileged: these "+
				"are ordinary binaries that run as the user that owns the state directory",
				strings.Join(missing, "; "), envPostgresBinDir),
		}
	}
	version := binaries[0]
	if out, err := exec.Command(binaries[0], "--version").CombinedOutput(); err == nil {
		version += ", " + firstLine(string(out))
	}
	if os.Geteuid() == 0 {
		return Result{
			Label: "host.postgres",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s, and this process runs as uid 0. initdb REFUSES to run as root, "+
				"so a host whose only user is root cannot create a per-cluster store at all: the "+
				"refusal happens before any file is written and it reads as a broken store rather "+
				"than as a rule. This is the same constraint inside a container -- the container has "+
				"to have a non-root user that owns the state directory, and running the container as "+
				"root does not make the store work, it only moves where it fails", version),
		}
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return Result{
			Label: "host.postgres",
			Kind:  Missing,
			Detail: fmt.Sprintf("a loopback port for the probe could not be taken: %v. The store "+
				"probe starts the real postgres the reconciler starts, and it does so on a port the "+
				"kernel just handed out rather than on a range this check guessed at", err),
		}
	}
	stateDir, err := os.MkdirTemp("", "check-host-postgres-")
	if err != nil {
		return Result{
			Label: "host.postgres",
			Kind:  Missing,
			Detail: fmt.Sprintf("a state directory for the probe could not be created: %v. A store "+
				"is a directory postgres must own and write; a host that cannot make one cannot hold "+
				"a cluster's objects", err),
		}
	}
	defer os.RemoveAll(stateDir)
	ctx, cancel := context.WithTimeout(context.Background(), postgresProbeTimeout)
	defer cancel()
	spec := clusterprovider.PostgresSpec{
		Cluster:  "check-host",
		StateDir: stateDir,
		Port:     port,
		BinDir:   f.postgresBinDir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	pg, err := clusterprovider.StartPostgres(ctx, spec)
	if err != nil {
		return Result{
			Label: "host.postgres",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s, and the store this project starts could not be started here: "+
				"%v. This probe runs the reconciler's OWN StartPostgres -- the same initdb argv, the "+
				"same password file, the same readiness query -- rather than a second definition of "+
				"one, so what failed here is what fails when a cluster is provisioned", version, err),
		}
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	stopped := "and it was stopped"
	if err := pg.Stop(stopCtx); err != nil {
		stopped = fmt.Sprintf("and stopping it reported %v", err)
	}
	return Result{
		Label: "host.postgres",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s; initdb created a data directory as uid %d, the server served %s "+
			"on 127.0.0.1:%d, %s", version, os.Geteuid(), spec.DataDir(), port, stopped),
	}
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("the kernel answered with %T rather than a TCP address", listener.Addr())
	}
	return addr.Port, nil
}
