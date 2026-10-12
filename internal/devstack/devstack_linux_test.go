//go:build linux

package devstack

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devsup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func TestMain(m *testing.M) {
	if IsStandIn() {
		os.Exit(StandInMain())
	}
	os.Exit(m.Run())
}

func standIns() Profile {
	return StandIns(Listener("store"), Listener("kine-a"), Listener("front"))
}

func invocations(units []devinstance.Unit) map[string]string {
	out := map[string]string{}
	for _, u := range units {
		out[u.Role] = u.Identity.InvocationID
	}
	return out
}

func TestTwoInstancesOnOneHostShareNothing(t *testing.T) {
	a := Start(t, standIns())
	b := Start(t, standIns())
	seen := map[string]string{}
	for _, s := range []*Stack{a, b} {
		vals := map[string]string{"socket": s.Up.Socket, "state": s.Up.StateDir, "devsup": filepath.Dir(s.Up.Socket)}
		for _, u := range s.Up.Units {
			if u.Port == 0 {
				t.Fatalf("%s %s reported no bound port", s.ID, u.Role)
			}
			vals["port "+u.Role] = u.Endpoint
			vals["unit "+u.Role] = u.Unit
		}
		for k, v := range vals {
			if prev, dup := seen[v]; dup {
				t.Fatalf("%s %s is shared with %s", k, v, prev)
			}
			seen[v] = s.ID + " " + k
		}
	}
	beforeB := invocations(b.Up.Units)
	r := a.Down(t)
	if !r.DevsupStopped || len(r.Remaining) > 0 {
		t.Fatalf("down %+v", r)
	}
	st := b.Status(t)
	after := map[string]string{}
	for _, u := range st.Devsup.Units {
		after[u.Name] = u.InvocationID
	}
	for _, u := range b.Up.Units {
		if after[u.Unit] != beforeB[u.Role] || u.Identity.Backend != hostsvc.BackendProc {
			t.Fatalf("stopping %s touched %s of %s: %s -> %s", a.ID, u.Unit, b.ID, beforeB[u.Role], after[u.Unit])
		}
	}
}

func TestASecondDevUpOfOneInstanceAdoptsItsDevsup(t *testing.T) {
	s := Start(t, standIns())
	again := s.UpAgain(t)
	if !again.Devsup.Adopted || again.Devsup.PID != s.Up.Devsup.PID {
		t.Fatalf("devsup %+v, first %+v", again.Devsup, s.Up.Devsup)
	}
	first := invocations(s.Up.Units)
	if len(again.Units) != len(first) {
		t.Fatalf("units %d, first %d", len(again.Units), len(first))
	}
	for _, u := range again.Units {
		if !u.Adopted || u.Identity.InvocationID != first[u.Role] {
			t.Fatalf("%s re-created by a second dev up: %+v", u.Role, u)
		}
	}
	if again.Core.PID != s.Up.Core.PID {
		t.Fatalf("a second dev up replaced the core: %d -> %d", s.Up.Core.PID, again.Core.PID)
	}
}

func TestDevDownLeavesNoProcess(t *testing.T) {
	s := Start(t, standIns())
	pids := []int{s.Up.Devsup.PID, s.Up.Core.PID}
	for _, u := range s.Up.Units {
		pids = append(pids, u.Identity.PID)
	}
	r := s.Down(t)
	if !r.DevsupStopped || len(r.Remaining) > 0 || !r.RemovedState || !r.RemovedRuntime {
		t.Fatalf("down %+v", r)
	}
	for _, p := range pids {
		if devsup.Alive(p) {
			t.Fatalf("pid %d outlived dev down", p)
		}
	}
	if left := devsup.InstanceProcesses(s.ID); len(left) > 0 {
		t.Fatalf("processes of %s left: %v", s.ID, left)
	}
	for _, p := range []string{s.Up.StateDir, filepath.Dir(s.Up.Socket)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", p, err)
		}
	}
}

func TestACoreRestartUnderDevAdoptsEveryUnit(t *testing.T) {
	s := Start(t, standIns())
	before := invocations(s.Up.Units)
	core := s.Up.Core
	if err := syscall.Kill(core.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	next := s.WaitCore(t, core.Generation, 30*time.Second)
	if len(next.Adoptions) != len(before) {
		t.Fatalf("generation %d adopted %d units, want %d", next.Generation, len(next.Adoptions), len(before))
	}
	for _, ad := range next.Adoptions {
		if !ad.Adopted || ad.InvocationID != before[ad.Role] {
			t.Fatalf("core generation %d re-created %s after kill -9: %+v, before %s", next.Generation, ad.Role, ad, before[ad.Role])
		}
	}
	r := s.RestartCore(t)
	if !r.UnitsUnchanged || r.After.Generation <= next.Generation {
		t.Fatalf("dev restart core %+v", r)
	}
	for _, ad := range r.After.Adoptions {
		if ad.InvocationID != before[ad.Role] {
			t.Fatalf("dev restart core re-created %s: %+v", ad.Role, ad)
		}
	}
}
