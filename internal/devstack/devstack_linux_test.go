//go:build linux

package devstack

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
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
