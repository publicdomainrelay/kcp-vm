//go:build linux

package hostsvc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

const (
	helperEnv       = "SOCIALWEB_HOSTSVC_HELPER"
	helperChildEnv  = "SOCIALWEB_HOSTSVC_CHILD"
	helperReportEnv = "SOCIALWEB_HOSTSVC_REPORT"
	helperAllocEnv  = "SOCIALWEB_HOSTSVC_ALLOC_MIB"
	standIn         = "/usr/bin/sleep"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "starter":
		os.Exit(runStarter())
	case "alloc":
		os.Exit(runAlloc())
	}
	os.Exit(m.Run())
}

func runStarter() int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m, err := Open(ctx, Config{Backend: BackendSystemdUser})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	st, err := m.Start(ctx, UnitSpec{Name: os.Getenv(helperChildEnv), Description: "hostsvc live test stand-in child",
		Exec: []string{standIn, "infinity"}, KillSignal: syscall.SIGKILL})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	f, err := os.OpenFile(os.Getenv(helperReportEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 4
	}
	fmt.Fprintf(f, "%v %s %d\n", st.Adopted, st.InvocationID, st.MainPID)
	f.Close()
	time.Sleep(time.Hour)
	return 0
}

func runAlloc() int {
	mib, _ := strconv.Atoi(os.Getenv(helperAllocEnv))
	time.Sleep(2 * time.Second)
	b := make([]byte, mib<<20)
	for i := 0; i < len(b); i += 4096 {
		b[i] = 1
	}
	time.Sleep(time.Minute)
	return int(b[0])
}

type live struct {
	t      *testing.T
	m      Manager
	prefix string
}

func openLive(t *testing.T) *live {
	t.Helper()
	livegate.Short(t, "this test starts stand-in transient services (sleep, the test binary) in this account's systemd user manager")
	if _, err := os.Stat(PrivateSocketPath()); err != nil {
		livegate.RequireLocal(t, "the user manager's private socket %s is missing: %v", PrivateSocketPath(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := Open(ctx, Config{Backend: BackendSystemdUser})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r := make([]byte, 4)
	rand.Read(r)
	l := &live{t: t, m: m, prefix: "socialweb-test-" + hex.EncodeToString(r) + "-"}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		units, err := m.List(ctx, l.prefix)
		if err != nil {
			t.Errorf("cleanup list: %v", err)
		}
		for _, u := range units {
			if err := m.Stop(ctx, u.Name); err != nil {
				t.Errorf("cleanup stop %s: %v", u.Name, err)
			}
			if err := m.ResetFailed(ctx, u.Name); err != nil {
				t.Errorf("cleanup reset %s: %v", u.Name, err)
			}
		}
		left, _ := m.List(ctx, l.prefix)
		if len(left) != 0 {
			t.Errorf("units left after cleanup: %+v", left)
		}
		l.reading("after cleanup")
		m.Close()
	})
	return l
}

func (l *live) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func (l *live) name(s string) string { return l.prefix + s + ServiceSuffix }

func (l *live) reading(label string) {
	l.t.Helper()
	out, err := exec.Command("systemctl", "--user", "list-units", "--all", "--no-legend", "--plain",
		l.prefix+"*").CombinedOutput()
	l.t.Logf("%s %s: systemctl --user list-units '%s*' (err=%v):\n%s",
		time.Now().UTC().Format(time.RFC3339), label, l.prefix, err, strings.TrimSpace(string(out)))
}

func (l *live) systemctl(args ...string) {
	l.t.Helper()
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		l.t.Fatalf("systemctl --user %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	l.t.Logf("%s systemctl --user %s", time.Now().UTC().Format(time.RFC3339), strings.Join(args, " "))
}

func (l *live) start(spec UnitSpec) UnitStatus {
	l.t.Helper()
	ctx, cancel := l.ctx()
	defer cancel()
	st, err := l.m.Start(ctx, spec)
	if err != nil {
		l.t.Fatalf("Start %s: %v", spec.Name, err)
	}
	return st
}

func (l *live) inspect(name string) UnitStatus {
	l.t.Helper()
	ctx, cancel := l.ctx()
	defer cancel()
	st, err := l.m.Inspect(ctx, name)
	if err != nil {
		l.t.Fatalf("Inspect %s: %v", name, err)
	}
	return st
}

func (l *live) identify(name string) Identity {
	l.t.Helper()
	ctx, cancel := l.ctx()
	defer cancel()
	id, err := l.m.Identify(ctx, name, 0)
	if err != nil {
		l.t.Fatalf("Identify %s: %v", name, err)
	}
	return id
}

func (l *live) check(want Identity) Observation {
	l.t.Helper()
	ctx, cancel := l.ctx()
	defer cancel()
	obs, err := l.m.Check(ctx, want)
	if err != nil {
		l.t.Fatalf("Check %s: %v", want.Unit, err)
	}
	return obs
}

func (l *live) until(what string, d time.Duration, f func() bool) {
	l.t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			l.t.Fatalf("%s: not within %s", what, d)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (l *live) running(want Identity, label string) {
	l.t.Helper()
	var obs Observation
	l.until(label+": "+want.Unit+" Running", 15*time.Second, func() bool {
		obs = l.check(want)
		return obs.Verdict != VerdictUnknown
	})
	if obs.Verdict != VerdictRunning {
		l.t.Fatalf("%s: %s is %s/%s (%s), want Running", label, want.Unit, obs.Verdict, obs.Reason, obs.Detail)
	}
	l.t.Logf("%s %s: %s Running invocation=%s pid=%d ticks=%d", time.Now().UTC().Format(time.RFC3339),
		label, want.Unit, obs.Status.InvocationID, want.PID, want.StartTicks)
}

func sleeper(name string) UnitSpec {
	return UnitSpec{Name: name, Description: "hostsvc live test stand-in", Exec: []string{standIn, "infinity"},
		KillSignal: syscall.SIGKILL, StopTimeout: 5 * time.Second}
}

func reportLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	return lines
}

func TestLiveAUnitSurvivesSystemctlRestartStopAndKill9OfItsStarter(t *testing.T) {
	l := openLive(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "starter.report")
	child := l.name("vm-child")
	starter := l.name("starter")
	l.start(UnitSpec{Name: starter, Description: "hostsvc live test starter", Exec: []string{exe, "-test.run=^$"},
		Env: []string{helperEnv + "=starter", helperChildEnv + "=" + child, helperReportEnv + "=" + report,
			"XDG_RUNTIME_DIR=" + os.Getenv("XDG_RUNTIME_DIR")},
		KillSignal: syscall.SIGTERM, StopTimeout: 5 * time.Second})
	wantLine := func(n int) []string {
		l.until(fmt.Sprintf("starter report line %d", n), 20*time.Second, func() bool { return len(reportLines(report)) >= n })
		return strings.Fields(reportLines(report)[n-1])
	}
	first := wantLine(1)
	if first[0] != "false" {
		t.Fatalf("the first starter adopted instead of starting: %v", first)
	}
	id := l.identify(child)
	if id.InvocationID != first[1] || strconv.Itoa(id.PID) != first[2] {
		t.Fatalf("starter reported %v, identity %+v", first, id)
	}
	l.reading("baseline")
	l.running(id, "baseline")

	l.systemctl("restart", starter)
	second := wantLine(2)
	if second[0] != "true" || second[1] != id.InvocationID || second[2] != first[2] {
		t.Fatalf("the restarted starter did not adopt the same child: %v, want true %s %s", second, id.InvocationID, first[2])
	}
	l.reading("after systemctl restart of the starter")
	l.running(id, "after restart")

	sp := l.inspect(starter)
	if err := syscall.Kill(sp.MainPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	l.until("starter failed after kill -9", 10*time.Second, func() bool { return l.inspect(starter).State == Failed })
	if st := l.inspect(starter); st.Result != "signal" || st.ExecMainStatus != int32(syscall.SIGKILL) {
		t.Fatalf("killed starter: %+v", st)
	}
	l.reading("after kill -9 of the starter")
	l.running(id, "after kill -9")

	l.systemctl("restart", starter)
	third := wantLine(3)
	if third[0] != "true" || third[1] != id.InvocationID {
		t.Fatalf("the starter restarted after kill -9 did not adopt: %v", third)
	}
	l.systemctl("stop", starter)
	l.until("starter gone after stop", 10*time.Second, func() bool {
		s := l.inspect(starter).State
		return s == NotFound || s == Inactive
	})
	l.reading("after systemctl stop of the starter")
	l.running(id, "after stop")
}

func TestLiveAUnitSurvivesDaemonReloadAndDaemonReexec(t *testing.T) {
	l := openLive(t)
	name := l.name("vm-reexec")
	l.start(sleeper(name))
	id := l.identify(name)
	l.reading("baseline")
	l.running(id, "baseline")
	l.systemctl("daemon-reload")
	l.reading("after daemon-reload")
	l.running(id, "after daemon-reload")
	l.systemctl("daemon-reexec")
	l.reading("after daemon-reexec")
	l.running(id, "after daemon-reexec")
	if again := l.identify(name); again != id {
		t.Fatalf("identity changed across reload/reexec: %+v -> %+v", id, again)
	}
}

func TestLiveAFailedUnitsResultIsReadableUntilReset(t *testing.T) {
	l := openLive(t)
	name := l.name("vm-killed")
	first := l.start(sleeper(name))
	if again := l.start(sleeper(name)); !again.Adopted || again.InvocationID != first.InvocationID {
		t.Fatalf("a second Start of a live name: %+v, want adopted %s", again, first.InvocationID)
	}
	id := l.identify(name)
	if err := syscall.Kill(id.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	l.until("unit failed after kill -9", 10*time.Second, func() bool { return l.inspect(name).State == Failed })
	l.reading("after kill -9 of the stand-in")
	time.Sleep(time.Second)
	st := l.inspect(name)
	if st.Result != "signal" || st.ExecMainCode != 2 || st.ExecMainStatus != int32(syscall.SIGKILL) {
		t.Fatalf("failed unit reads %+v, want result signal, code 2 (killed), status 9", st)
	}
	if obs := l.check(id); obs.Verdict != VerdictLost || obs.Reason != LostUnitFailed || obs.Stopped {
		t.Fatalf("Check of a failed unit: %+v", obs)
	}
	ctx, cancel := l.ctx()
	defer cancel()
	if err := l.m.ResetFailed(ctx, name); err != nil {
		t.Fatal(err)
	}
	if st := l.inspect(name); st.State != NotFound {
		t.Fatalf("after reset-failed: %+v", st)
	}
	if obs := l.check(id); obs.Verdict != VerdictLost || obs.Reason != LostUnitGone {
		t.Fatalf("Check after reset: %+v", obs)
	}
	l.reading("after reset-failed")
}

func TestLiveMemoryMaxWithSwapZeroIsAnOomKillNotSwap(t *testing.T) {
	l := openLive(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := l.name("vm-oom")
	st := l.start(UnitSpec{Name: name, Description: "hostsvc live test memory stand-in", Exec: []string{exe, "-test.run=^$"},
		Env:    []string{helperEnv + "=alloc", helperAllocEnv + "=256"},
		Limits: Limits{MemoryMax: 64 << 20, TasksMax: 32}, KillSignal: syscall.SIGKILL})
	cg, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", st.MainPID))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(strings.TrimSpace(string(cg)), "0::"))
	for file, want := range map[string]string{"memory.max": "67108864", "memory.swap.max": "0", "pids.max": "32"} {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || strings.TrimSpace(string(b)) != want {
			t.Fatalf("%s/%s = %q (%v), want %s", dir, file, b, err, want)
		}
	}
	if !strings.Contains(dir, "/"+DefaultSlice+"/") || strings.Contains(dir, "/app.slice/") {
		t.Fatalf("unit cgroup %s is not under %s", dir, DefaultSlice)
	}
	t.Logf("%s cgroup %s memory.max=64MiB memory.swap.max=0", time.Now().UTC().Format(time.RFC3339), dir)
	l.until("oom-kill", 30*time.Second, func() bool { return l.inspect(name).State == Failed })
	got := l.inspect(name)
	l.reading("after the allocation")
	if got.Result != "oom-kill" {
		t.Fatalf("a 256MiB allocation under MemoryMax=64MiB with swap 0 ended %+v, want result oom-kill", got)
	}
}

func TestLiveUnitsOfAnotherPrefixAreNeverListed(t *testing.T) {
	l := openLive(t)
	for _, n := range []string{"a-x", "ab-y", "b-z"} {
		l.start(sleeper(l.name(n)))
	}
	ctx, cancel := l.ctx()
	defer cancel()
	got, err := l.m.List(ctx, l.prefix+"a-")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != l.name("a-x") || got[0].State != Active {
		t.Fatalf("List(%sa-) = %+v", l.prefix, got)
	}
	l.reading("three units, one listed")
}

func TestLiveAMismatchOnAnActiveUnitIsLostAndStoppedNow(t *testing.T) {
	l := openLive(t)
	name := l.name("vm-mismatch")
	l.start(sleeper(name))
	id := l.identify(name)
	l.running(id, "baseline")
	ticks, err := startTicks(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	impostor := id
	impostor.PID, impostor.StartTicks = os.Getpid(), ticks
	obs := l.check(impostor)
	if obs.Verdict != VerdictLost || obs.Reason != LostIdentityMismatch || !obs.Stopped {
		t.Fatalf("a recorded pid in another unit: %+v", obs)
	}
	t.Logf("%s mismatch: %s", time.Now().UTC().Format(time.RFC3339), obs.Detail)
	after := l.inspect(name)
	switch {
	case after.State == Failed && after.Result == "signal" && after.ExecMainStatus == int32(syscall.SIGKILL):
		t.Logf("a stop under KillSignal=SIGKILL leaves the unit failed/signal until ResetFailed: %+v", after)
	case after.State == NotFound || after.State == Inactive:
	default:
		t.Fatalf("%s is %+v after a mismatch, want stopped", name, after)
	}
	if err := syscall.Kill(id.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the stand-in pid %d still answers signal 0 after the mismatch stop: %v", id.PID, err)
	}
	l.reading("after the mismatch stop")
}
