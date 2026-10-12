//go:build linux

package devsup

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func marker() string { return strconv.Itoa(200000 + int(time.Now().UnixNano()%700000)) }

func pidsWithArg(arg string) []int {
	var out []int
	for _, p := range procTable() {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/cmdline")
		if err != nil {
			continue
		}
		for _, a := range strings.Split(string(b), "\x00") {
			if a == arg {
				out = append(out, p.pid)
			}
		}
	}
	return out
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func mustOpen(t *testing.T, d *devsupProc) hostsvc.Manager {
	t.Helper()
	m, err := d.open(t)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sh(t *testing.T) string {
	p, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestADevsupCrashLeavesNoProcessAndTheNextDevsupSweepsByLeaderTicks(t *testing.T) {
	d := launchDevsup(t, shortDir(t), false)
	m := mustOpen(t, d)
	grand, leader := marker(), marker()+"1"
	st, err := m.Start(context.Background(), hostsvc.UnitSpec{Name: "socialweb-test-crash-tree.service",
		Exec: []string{sh(t), "-c", "sleep " + grand + " & exec sleep " + leader}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the grandchild", 5*time.Second, func() bool { return len(pidsWithArg(grand)) == 1 })
	g := pidsWithArg(grand)[0]
	d.kill()
	waitFor(t, "the unit's main process to die with devsup (Pdeathsig)", 5*time.Second, func() bool { return !alivePID(st.MainPID) })
	if !alivePID(g) {
		t.Fatalf("the grandchild %d died with devsup; this test needs the leak it guards against", g)
	}
	t.Logf("reading %s: devsup kill -9; leader %d gone, grandchild %d (pgid %d) survived", time.Now().UTC().Format(time.RFC3339Nano), st.MainPID, g, st.MainPID)
	d.start(t)
	waitFor(t, "the next devsup to sweep the grandchild by the recorded group", 5*time.Second, func() bool { return !alivePID(g) })
	b, _ := os.ReadFile(d.dir + "/devsup.log")
	if !strings.Contains(string(b), "signal the group") {
		t.Fatalf("the sweep did not record signalling the group:\n%s", b)
	}
	t.Logf("reading %s: next devsup swept grandchild %d by pgid %d", time.Now().UTC().Format(time.RFC3339Nano), g, st.MainPID)
}

func TestAPgidWhoseLeaderPidWasReusedIsNeverSignalled(t *testing.T) {
	rec := Record{PGID: 4242, LeaderTicks: 100}
	cases := []struct {
		name  string
		table []ProcEntry
		want  SweepVerdict
	}{
		{"leader alive with its ticks", []ProcEntry{{PID: 4242, PGID: 4242, Ticks: 100}}, SweepGroup},
		{"leader pid reused by another process", []ProcEntry{{PID: 4242, PGID: 9000, Ticks: 555}}, SweepRefuseReused},
		{"leader gone, members left", []ProcEntry{{PID: 4300, PGID: 4242, Ticks: 120}}, SweepGroup},
		{"nothing left", []ProcEntry{{PID: 1, PGID: 1, Ticks: 1}}, SweepNothing},
	}
	for _, c := range cases {
		if got := SweepDecision(rec, c.table); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDieWithStopsEverythingWhenTheWatchedPidExits(t *testing.T) {
	watched := exec.Command("sleep", "60")
	if err := watched.Start(); err != nil {
		t.Fatal(err)
	}
	d := launchDevsupDieWith(t, shortDir(t), false, watched.Process.Pid)
	m := mustOpen(t, d)
	st, err := m.Start(context.Background(), hostsvc.UnitSpec{Name: "socialweb-test-diewith.service",
		Exec: []string{sh(t), "-c", "exec sleep 1000"}})
	if err != nil {
		t.Fatal(err)
	}
	watched.Process.Kill()
	watched.Wait()
	select {
	case <-d.waited:
	case <-time.After(10 * time.Second):
		t.Fatalf("devsup outlived the pid it dies with")
	}
	if alivePID(st.MainPID) {
		t.Fatalf("unit pid %d outlived devsup", st.MainPID)
	}
	if _, err := os.Stat(d.socket); !os.IsNotExist(err) {
		t.Fatalf("devsup left its socket: %v", err)
	}
}

func TestAnOrphanReparentedToDevsupIsKilledWhenItsUnitIsInactive(t *testing.T) {
	d := launchDevsup(t, shortDir(t), false)
	m := mustOpen(t, d)
	orphan := marker()
	if _, err := m.Start(context.Background(), hostsvc.UnitSpec{Name: "socialweb-test-orphan.service",
		Exec: []string{sh(t), "-c", "setsid sleep " + orphan + " & sleep 0.3; exit 0"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the orphan to start", 5*time.Second, func() bool { return len(pidsWithArg(orphan)) == 1 })
	pid := pidsWithArg(orphan)[0]
	waitFor(t, "the unit to end", 5*time.Second, func() bool {
		st, _ := m.Inspect(context.Background(), "socialweb-test-orphan.service")
		return st.State == hostsvc.NotFound
	})
	waitFor(t, "the orphan of the inactive unit to be killed", 5*time.Second, func() bool { return !alivePID(pid) })
}

func TestAUnitPlacedByOneBackendIsLostOnTheOther(t *testing.T) {
	d := launchDevsup(t, shortDir(t), false)
	m := mustOpen(t, d)
	if _, err := m.Start(context.Background(), hostsvc.UnitSpec{Name: "socialweb-test-foreign.service",
		Exec: []string{sh(t), "-c", "exec sleep 1000"}}); err != nil {
		t.Fatal(err)
	}
	id, err := m.Identify(context.Background(), "socialweb-test-foreign.service", 0)
	if err != nil {
		t.Fatal(err)
	}
	id.Backend = hostsvc.BackendSystemdUser
	obs, err := m.Check(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Verdict != hostsvc.VerdictLost || obs.Reason != hostsvc.LostIdentityMismatch ||
		!strings.Contains(obs.Detail, "systemd-user") || !strings.Contains(obs.Detail, "proc") {
		t.Fatalf("a systemd-user identity on proc reads %+v", obs)
	}
	if st, _ := m.Inspect(context.Background(), "socialweb-test-foreign.service"); st.State != hostsvc.Active {
		t.Fatalf("the proc unit was touched by a foreign identity check: %+v", st)
	}
}
