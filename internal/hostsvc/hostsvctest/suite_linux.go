//go:build linux

package hostsvctest

import (
	"context"
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

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func Run(t *testing.T, h Harness) {
	for _, c := range Cases() {
		t.Run(c.Name, func(t *testing.T) { c.Run(t, h) })
	}
}

func Cases() []Case {
	return []Case{
		{"StartInspectStop", []string{"Start", "Inspect", "Stop", "Capabilities"}, caseStartInspectStop},
		{"AdoptNotRecreate", []string{"Start"}, caseAdopt},
		{"FailedReadableUntilResetFailed", []string{"ResetFailed", "Inspect"}, caseFailedUntilReset},
		{"KilledUnitReportsSignal9", []string{"Inspect"}, caseKilled},
		{"SurvivesTheStartersRestart", []string{"Close", "Identify", "Check"}, caseSurvivesStarter},
		{"StopKillsASetsidDescendant", []string{"Stop"}, caseSetsidDescendant},
		{"RestartOnFailureGetsANewInvocation", []string{"Start"}, caseRestartOnFailure},
		{"WatchReportsAnExitWithin2s", []string{"Watch"}, caseWatch},
		{"StopSliceStopsNestedSlicesAndNothingElse", []string{"StopSlice"}, caseStopSlice},
		{"ListNeverCrossesPrefixes", []string{"List"}, caseList},
		{"IdentityMismatchOnAReusedPid", []string{"Identify", "Check"}, caseMismatch},
		{"ManagerRestartLosesEveryUnit", []string{"Check"}, caseManagerRestart},
		{"LimitsGatedOnEnforcesLimits", []string{"Start", "Capabilities"}, caseLimits},
		{"FDStoreGatedOnCapability", []string{"Start", "Capabilities"}, caseFDStore},
		{"PrivateHostnameGatedOnCapability", []string{"Start", "Capabilities"}, casePrivateHostname},
	}
}

func bin(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("stand-in %s: %v", name, err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func name(h Harness, s string) string { return h.Prefix + s + hostsvc.ServiceSuffix }

func open(t *testing.T, h Harness) hostsvc.Manager {
	t.Helper()
	m, err := h.Open(t)
	if err != nil {
		t.Fatalf("opening the manager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func start(t *testing.T, h Harness, m hostsvc.Manager, spec hostsvc.UnitSpec) hostsvc.UnitStatus {
	t.Helper()
	st, err := m.Start(ctx(t), spec)
	if err != nil {
		t.Fatalf("Start %s: %v", spec.Name, err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		mm, err := h.Open(t)
		if err != nil {
			return
		}
		defer mm.Close()
		mm.Stop(c, spec.Name)
		mm.ResetFailed(c, spec.Name)
	})
	return st
}

func sleeper(t *testing.T, h Harness, short string) hostsvc.UnitSpec {
	return hostsvc.UnitSpec{Name: name(h, short), Exec: []string{bin(t, "sleep"), "1000"}, StopTimeout: 5 * time.Second}
}

func shell(t *testing.T, h Harness, short, script string) hostsvc.UnitSpec {
	return hostsvc.UnitSpec{Name: name(h, short), Exec: []string{bin(t, "sh"), "-c", script}, StopTimeout: 5 * time.Second}
}

func until(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func inspect(t *testing.T, m hostsvc.Manager, n string) hostsvc.UnitStatus {
	t.Helper()
	st, err := m.Inspect(ctx(t), n)
	if err != nil {
		t.Fatalf("Inspect %s: %v", n, err)
	}
	return st
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(b), ')')
	return i > 0 && len(b) > i+2 && b[i+2] != 'Z' && b[i+2] != 'X'
}

func pidsWithArg(arg string) []int {
	entries, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		for _, a := range strings.Split(string(b), "\x00") {
			if a == arg && alive(pid) {
				out = append(out, pid)
			}
		}
	}
	return out
}

func caseStartInspectStop(t *testing.T, h Harness) {
	m := open(t, h)
	caps := m.Capabilities()
	if caps.Backend == "" {
		t.Fatalf("Capabilities names no backend")
	}
	spec := sleeper(t, h, "sis")
	st := start(t, h, m, spec)
	if st.State != hostsvc.Active || st.MainPID <= 0 || st.InvocationID == "" || st.Adopted {
		t.Fatalf("started %+v", st)
	}
	if st.Backend != caps.Backend {
		t.Fatalf("status records backend %q, the manager is %q", st.Backend, caps.Backend)
	}
	got := inspect(t, m, spec.Name)
	if got.InvocationID != st.InvocationID || got.MainPID != st.MainPID || got.State != hostsvc.Active {
		t.Fatalf("inspect %+v after start %+v", got, st)
	}
	if err := m.Stop(ctx(t), spec.Name); err != nil {
		t.Fatal(err)
	}
	until(t, "the stopped unit to be collected", 5*time.Second, func() bool {
		return inspect(t, m, spec.Name).State == hostsvc.NotFound
	})
	if alive(st.MainPID) {
		t.Fatalf("pid %d outlived Stop", st.MainPID)
	}
	if err := m.Stop(ctx(t), spec.Name); err != nil {
		t.Fatalf("a second Stop of a gone unit: %v", err)
	}
}

func caseAdopt(t *testing.T, h Harness) {
	m := open(t, h)
	spec := sleeper(t, h, "adopt")
	first := start(t, h, m, spec)
	second, err := m.Start(ctx(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Adopted || second.InvocationID != first.InvocationID || second.MainPID != first.MainPID {
		t.Fatalf("second Start %+v, first %+v: want adopted, same invocation and pid", second, first)
	}
}

func caseFailedUntilReset(t *testing.T, h Harness) {
	m := open(t, h)
	spec := shell(t, h, "fail", "exit 3")
	start(t, h, m, spec)
	until(t, "the unit to fail", 5*time.Second, func() bool { return inspect(t, m, spec.Name).State == hostsvc.Failed })
	time.Sleep(500 * time.Millisecond)
	st := inspect(t, m, spec.Name)
	if st.State != hostsvc.Failed || st.Result != "exit-code" || st.ExecMainStatus != 3 {
		t.Fatalf("failed unit reads %+v, want failed/exit-code/3", st)
	}
	if err := m.ResetFailed(ctx(t), spec.Name); err != nil {
		t.Fatal(err)
	}
	until(t, "the reset unit to be collected", 5*time.Second, func() bool {
		return inspect(t, m, spec.Name).State == hostsvc.NotFound
	})
}

func caseKilled(t *testing.T, h Harness) {
	m := open(t, h)
	spec := sleeper(t, h, "kill9")
	st := start(t, h, m, spec)
	if err := syscall.Kill(st.MainPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	until(t, "the killed unit to fail", 5*time.Second, func() bool { return inspect(t, m, spec.Name).State == hostsvc.Failed })
	got := inspect(t, m, spec.Name)
	if got.Result != "signal" || got.ExecMainCode != 2 || got.ExecMainStatus != 9 {
		t.Fatalf("killed unit reads %+v, want signal/2/9", got)
	}
}

func caseSurvivesStarter(t *testing.T, h Harness) {
	m1 := open(t, h)
	spec := sleeper(t, h, "starter")
	st := start(t, h, m1, spec)
	id, err := m1.Identify(ctx(t), spec.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id.Backend != m1.Capabilities().Backend || id.Manager == "" || id.PID != st.MainPID {
		t.Fatalf("identity %+v", id)
	}
	if err := m1.Close(); err != nil {
		t.Fatal(err)
	}
	m2 := open(t, h)
	got := inspect(t, m2, spec.Name)
	if got.InvocationID != st.InvocationID || got.MainPID != st.MainPID {
		t.Fatalf("after the starter closed: %+v, before %+v", got, st)
	}
	obs, err := m2.Check(ctx(t), id)
	if err != nil || obs.Verdict != hostsvc.VerdictRunning {
		t.Fatalf("Check from a new client: %+v %v", obs, err)
	}
}

func caseSetsidDescendant(t *testing.T, h Harness) {
	m := open(t, h)
	marker := strconv.Itoa(100000 + int(time.Now().UnixNano()%800000))
	spec := shell(t, h, "setsid", "setsid sleep "+marker+" & exec sleep 1001")
	start(t, h, m, spec)
	var escaped []int
	until(t, "the setsid descendant to start", 5*time.Second, func() bool {
		escaped = pidsWithArg(marker)
		return len(escaped) == 1
	})
	if err := m.Stop(ctx(t), spec.Name); err != nil {
		t.Fatal(err)
	}
	until(t, "the setsid descendant to die with its unit", 5*time.Second, func() bool { return !alive(escaped[0]) })
}

func caseRestartOnFailure(t *testing.T, h Harness) {
	m := open(t, h)
	spec := shell(t, h, "restart", "sleep 1; exit 1")
	spec.Restart = hostsvc.RestartOnFailure
	first := start(t, h, m, spec)
	var again hostsvc.UnitStatus
	until(t, "a restart with a new invocation", 10*time.Second, func() bool {
		again = inspect(t, m, spec.Name)
		return again.State == hostsvc.Active && again.InvocationID != "" && again.InvocationID != first.InvocationID
	})
	if again.MainPID == first.MainPID {
		t.Fatalf("restarted on the same pid %d", again.MainPID)
	}
}

func caseWatch(t *testing.T, h Harness) {
	m := open(t, h)
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	events, err := m.Watch(wctx, h.Prefix+"watch")
	if err != nil {
		t.Fatal(err)
	}
	spec := shell(t, h, "watch", "sleep 1; exit 4")
	start(t, h, m, spec)
	exitAt := time.Now().Add(time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("the watch closed before the exit")
			}
			if ev.Name == spec.Name && ev.Status.State == hostsvc.Failed {
				if late := time.Since(exitAt); late > 2*time.Second {
					t.Fatalf("the exit was reported %s after it happened", late)
				}
				return
			}
		case <-wctx.Done():
			t.Fatalf("no failed event for %s", spec.Name)
		}
	}
}

func caseStopSlice(t *testing.T, h Harness) {
	m := open(t, h)
	parent := h.SlicePrefix + ".slice"
	nested := h.SlicePrefix + "-inner.slice"
	sibling := h.SlicePrefix + "x.slice"
	a, b, c := sleeper(t, h, "slice-a"), sleeper(t, h, "slice-b"), sleeper(t, h, "slice-c")
	a.Slice, b.Slice, c.Slice = parent, nested, sibling
	t.Cleanup(func() {
		if mm, err := h.Open(t); err == nil {
			mm.StopSlice(context.Background(), sibling)
			mm.StopSlice(context.Background(), parent)
			mm.Close()
		}
	})
	start(t, h, m, a)
	start(t, h, m, b)
	cst := start(t, h, m, c)
	if err := m.StopSlice(ctx(t), parent); err != nil {
		t.Fatal(err)
	}
	until(t, "the parent and nested slice units to be gone", 5*time.Second, func() bool {
		return inspect(t, m, a.Name).State == hostsvc.NotFound && inspect(t, m, b.Name).State == hostsvc.NotFound
	})
	got := inspect(t, m, c.Name)
	if got.State != hostsvc.Active || got.InvocationID != cst.InvocationID {
		t.Fatalf("the sibling slice's unit was touched: %+v", got)
	}
	if err := m.StopSlice(ctx(t), "app.slice"); err == nil {
		t.Fatalf("StopSlice accepted a slice that is not ours")
	}
}

func caseList(t *testing.T, h Harness) {
	m := open(t, h)
	start(t, h, m, sleeper(t, h, "lista-1"))
	start(t, h, m, sleeper(t, h, "listb-1"))
	got, err := m.List(ctx(t), h.Prefix+"lista-")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != name(h, "lista-1") {
		t.Fatalf("List %slista- = %+v", h.Prefix, got)
	}
	if _, err := m.List(ctx(t), "other-"); err == nil {
		t.Fatalf("List accepted a prefix that is not ours")
	}
}

func caseMismatch(t *testing.T, h Harness) {
	m := open(t, h)
	spec := sleeper(t, h, "mismatch")
	st := start(t, h, m, spec)
	id, err := m.Identify(ctx(t), spec.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	id.StartTicks++
	obs, err := m.Check(ctx(t), id)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Verdict != hostsvc.VerdictLost || obs.Reason != hostsvc.LostIdentityMismatch || !obs.Stopped {
		t.Fatalf("a reused pid reads %+v, want Lost(IdentityMismatch) and stopped", obs)
	}
	until(t, "the mismatched unit's pid to die", 5*time.Second, func() bool { return !alive(st.MainPID) })
}

func caseManagerRestart(t *testing.T, h Harness) {
	if h.RestartManager == nil {
		t.Skip(h.SkipManagerRestart)
	}
	m := open(t, h)
	spec := sleeper(t, h, "mgr")
	st := start(t, h, m, spec)
	id, err := m.Identify(ctx(t), spec.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.RestartManager(t)
	m2 := open(t, h)
	obs, err := m2.Check(ctx(t), id)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Verdict != hostsvc.VerdictLost || obs.Reason != hostsvc.LostUnitGone {
		t.Fatalf("after a manager restart: %+v, want Lost(UnitGone)", obs)
	}
	if got := inspect(t, m2, spec.Name); got.State != hostsvc.NotFound {
		t.Fatalf("the unit outlived its manager: %+v", got)
	}
	until(t, "the unit's pid to be gone", 5*time.Second, func() bool { return !alive(st.MainPID) })
}

func cgroupFile(pid int, file string) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "0::") {
			v, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"), file))
			return strings.TrimSpace(string(v)), err
		}
	}
	return "", errors.New("no cgroup v2 line")
}

func caseLimits(t *testing.T, h Harness) {
	m := open(t, h)
	if !m.Capabilities().EnforcesLimits {
		t.Skipf("the %s backend declares EnforcesLimits=false: limits are accepted and not enforced", m.Capabilities().Backend)
	}
	spec := sleeper(t, h, "limits")
	spec.Limits.MemoryMax = 64 << 20
	st := start(t, h, m, spec)
	got, err := cgroupFile(st.MainPID, "memory.max")
	if err != nil || got != fmt.Sprint(64<<20) {
		t.Fatalf("memory.max of the unit's cgroup = %q (%v), want %d", got, err, 64<<20)
	}
}

func caseFDStore(t *testing.T, h Harness) {
	m := open(t, h)
	spec := sleeper(t, h, "fdstore")
	spec.FDStore = 1
	if m.Capabilities().FDStore {
		start(t, h, m, spec)
		return
	}
	if _, err := m.Start(ctx(t), spec); !errors.Is(err, hostsvc.ErrUnsupported) {
		t.Fatalf("an fd store on a backend without one: %v, want ErrUnsupported", err)
	}
}

func casePrivateHostname(t *testing.T, h Harness) {
	m := open(t, h)
	spec := sleeper(t, h, "hostname")
	spec.PrivateHostname = "slot-a"
	if m.Capabilities().PrivateHostname {
		start(t, h, m, spec)
		return
	}
	if _, err := m.Start(ctx(t), spec); !errors.Is(err, hostsvc.ErrUnsupported) {
		t.Fatalf("a private hostname on a backend without one: %v, want ErrUnsupported", err)
	}
}
