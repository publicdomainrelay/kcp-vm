//go:build linux

package treeaudit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devsup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

const (
	envDevsupState  = "SWC_TREEAUDIT_DEVSUP_STATE"
	envDevsupSocket = "SWC_TREEAUDIT_DEVSUP_SOCKET"
	envHold         = "SWC_TREEAUDIT_HOLD"
)

func TestMain(m *testing.M) {
	if os.Getenv(envHold) == "1" {
		_, _ = syscall.Setsid()
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if state := os.Getenv(envDevsupState); state != "" {
		if err := devsup.Run(context.Background(), devsup.Options{State: state, Socket: os.Getenv(envDevsupSocket), DieWith: os.Getppid()}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func suffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func standIn(t *testing.T, dir string) (string, map[string]string) {
	t.Helper()
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep on PATH for the stand-in VMM")
	}
	dst := filepath.Join(dir, "firecracker")
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
	return dst, map[string]string{sum(t, dst): "firecracker (stand-in)"}
}

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp(os.Getenv("XDG_RUNTIME_DIR"), "swc-ta")
	if err != nil {
		d = t.TempDir()
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func startDevsup(t *testing.T) (hostsvc.Manager, int) {
	t.Helper()
	dir := shortDir(t)
	socket := filepath.Join(dir, "sup.sock")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), envDevsupState+"="+filepath.Join(dir, "sup"), envDevsupSocket+"="+socket)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := hostsvc.OpenProcClient(context.Background(), hostsvc.Config{Backend: hostsvc.BackendProc, ProcSocket: socket})
		if err == nil {
			if h, herr := c.Hello(context.Background()); herr == nil && h.PID == cmd.Process.Pid {
				return c, h.PID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("devsup did not answer on %s: %v", socket, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func only(r Report, name string) Report {
	out := Report{Backend: r.Backend}
	for _, u := range r.Units {
		if u.Name == name {
			out.Units = append(out.Units, u)
		}
	}
	for _, f := range r.Findings {
		if f.Unit == name {
			out.Findings = append(out.Findings, f)
		}
	}
	return out
}

func waitAudit(t *testing.T, m hostsvc.Manager, devsupPID int, pinned map[string]string, name string, done func(Report) bool) Report {
	t.Helper()
	var r Report
	deadline := time.Now().Add(10 * time.Second)
	for {
		full, err := Audit(context.Background(), m, Host{}, devsupPID, pinned)
		if err != nil {
			t.Fatal(err)
		}
		r = only(full, name)
		if done(r) || time.Now().After(deadline) {
			return r
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTheAuditPassesOnTheProcBackendWithDevsupOutsideTheGroup(t *testing.T) {
	m, devsupPID := startDevsup(t)
	vmm, pinned := standIn(t, shortDir(t))
	name := VMPrefix + "ta" + suffix() + "-guest.service"
	if _, err := m.Start(context.Background(), hostsvc.UnitSpec{Name: name, Exec: []string{vmm, "60"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background(), name) })
	r := waitAudit(t, m, devsupPID, pinned, name, func(r Report) bool { return r.Pass() && len(r.Units) == 1 })
	if !r.Pass() || len(r.Units) != 1 {
		t.Fatalf("report %+v", r)
	}
	for _, p := range r.Units[0].Procs {
		if p.PID == devsupPID {
			t.Fatalf("devsup %d is counted as a member of %s", devsupPID, name)
		}
	}
}

func TestAnAttributedDescendantRunningTheCoreBinaryFailsOnProc(t *testing.T) {
	m, devsupPID := startDevsup(t)
	vmm, pinned := standIn(t, shortDir(t))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := VMPrefix + "ta" + suffix() + "-guest.service"
	spec := hostsvc.UnitSpec{Name: name, Env: []string{envHold + "=1"},
		Exec: []string{"/bin/sh", "-c", "'" + self + "' -test.run=^$ & exec '" + vmm + "' 60"}}
	if _, err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background(), name) })
	r := waitAudit(t, m, devsupPID, pinned, name, func(r Report) bool {
		return len(r.Findings) == 1 && strings.Contains(r.Findings[0].Exe, filepath.Base(self))
	})
	if len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Exe, filepath.Base(self)) {
		t.Fatalf("a setsid descendant running the core stand-in: findings %+v units %+v", r.Findings, r.Units)
	}
}

func TestTheAuditReadsTheRealCgroupOfAStandInUnit(t *testing.T) {
	livegate.Short(t, "this test starts a socialweb-vm-* stand-in transient service in this account's systemd user manager")
	if _, err := os.Stat(hostsvc.PrivateSocketPath()); err != nil {
		livegate.RequireLocal(t, "the user manager's private socket is missing: %v", err)
	}
	m, err := hostsvc.Open(context.Background(), hostsvc.Config{Backend: hostsvc.BackendSystemdUser})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	vmm, pinned := standIn(t, t.TempDir())
	name := VMPrefix + "ta" + suffix() + "-guest.service"
	if _, err := m.Start(context.Background(), hostsvc.UnitSpec{Name: name, Exec: []string{vmm, "60"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background(), name) })
	r := waitAudit(t, m, 0, pinned, name, func(r Report) bool { return r.Pass() && len(r.Units) == 1 && len(r.Units[0].Procs) == 1 })
	if !r.Pass() || len(r.Units) != 1 || len(r.Units[0].Procs) != 1 || r.Units[0].Procs[0].PID != r.Units[0].MainPID {
		t.Fatalf("report %+v", r)
	}
	delete(pinned, r.Units[0].Procs[0].SHA256)
	if r := waitAudit(t, m, 0, pinned, name, func(r Report) bool { return !r.Pass() }); r.Pass() {
		t.Fatal("an unpinned stand-in passed")
	}
}
