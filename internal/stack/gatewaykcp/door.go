package gatewaykcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

const (
	DoorEntry     = "hono-socialweb-computer-k8s"
	DoorDir       = "door"
	DoorWebDir    = "socialweb-computer-k8s/web"
	doorPortFile  = "port"
	doorStopGrace = 10 * time.Second
	doorPortPoll  = 100 * time.Millisecond
)

var doorPrograms = []string{"ssh-keygen"}

func (g *gateway) doorPermissions(state string, e *env) []string {
	run := append([]string(nil), doorPrograms...)
	if p := e.get("SOCIALWEB_CLUSTERPROVIDER_DUMBPIPE"); filepath.IsAbs(p) {
		run = append(run, p)
	}
	return []string{"--allow-net", "--allow-env", "--allow-sys", "--allow-read", "--allow-write=" + state,
		"--allow-run=" + strings.Join(run, ",")}
}

func (g *gateway) door(ctx context.Context, base *env, rec devinstance.Record, gw string) error {
	deno := g.c.Executables[ExeDeno].Path
	if deno == "" {
		return errors.New("no deno executable")
	}
	entries, err := manifests.LoadEntries()
	if err != nil {
		return err
	}
	entry, ok := entries[DoorEntry]
	if !ok {
		return fmt.Errorf("deploy/ts-entries.yaml names no %s", DoorEntry)
	}
	state := filepath.Join(g.c.StateDir, DoorDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	portFile := filepath.Join(state, doorPortFile)
	os.Remove(portFile)
	host, port, err := net.SplitHostPort(g.c.DoorAddress)
	if err != nil {
		return fmt.Errorf("door address %q: %w", g.c.DoorAddress, err)
	}
	e := newEnv(base.list())
	e.set("SERVE_ADDR", host)
	e.set("HTTP_PORT", port)
	e.set("PORT_FILE", portFile)
	e.set("STATE_DIR", state)
	e.set("WEB_DIR", filepath.Join(g.c.Closure.Root, filepath.FromSlash(DoorWebDir)))
	e.set("GATEWAY_URL", "http://"+gw)
	first, _, _ := strings.Cut(e.get("SOCIALWEB_GATEWAY_AUDIENCES"), ",")
	e.dflt("GATEWAY_AUDIENCE", strings.TrimSpace(first))
	e.dflt("KCP_KUBECONFIG", base.get("SOCIALWEB_KCP_KUBECONFIG"))
	e.dflt("PLC_DIRECTORY_URL", e.get("SOCIALWEB_GATEWAY_PLC_URL"))
	if h := e.get("SOCIALWEB_GATEWAY_KCP_PUBLIC_HOST"); h != "" {
		e.dflt("KCP_PUBLIC_HOST", h)
	}
	spec := g.c.Closure.Spec(deno, entry, nil)
	spec.Permissions = g.doorPermissions(state, e)
	cmd, err := spec.Command(ctx, e.list())
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(g.l.Logs, "door.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = doorStopGrace
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	g.c.log().Info("the door is a deno child", "pid", cmd.Process.Pid, "entry", spec.Entry.Path, "log", logf.Name())
	for {
		if b, err := os.ReadFile(portFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				a := &net.TCPAddr{IP: net.ParseIP(host), Port: n}
				if a.IP == nil {
					a.IP = net.IPv4(127, 0, 0, 1)
				}
				g.reg.set(ListenerDoor, "http", a)
				g.c.log().Info("the door listens", "addr", a.String())
				break
			}
		}
		select {
		case err := <-done:
			return fmt.Errorf("the door exited before it listened: %v", err)
		case <-time.After(doorPortPoll):
		}
	}
	err = <-done
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		err = errors.New("the door exited")
	}
	return err
}
