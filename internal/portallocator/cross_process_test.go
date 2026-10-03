package portallocator

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	childSentinel = "PORTALLOCATOR_CROSS_PROCESS_CHILD"
	childLease    = "PORTALLOCATOR_CROSS_PROCESS_LEASE"
	childRange    = "PORTALLOCATOR_CROSS_PROCESS_RANGE"
	childName     = "PORTALLOCATOR_CROSS_PROCESS_NAME"
	childBarrier  = "PORTALLOCATOR_CROSS_PROCESS_BARRIER"
)

const childPortLine = "PORTALLOCATOR_CHILD_PORT="

func TestMain(m *testing.M) {
	if os.Getenv(childSentinel) != "" {
		runChild()
		return
	}
	os.Exit(m.Run())
}

func runChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "cross-process child: "+format+"\n", args...)
		os.Exit(1)
	}
	leasePath := os.Getenv(childLease)
	rng, err := parseChildRange(os.Getenv(childRange))
	if err != nil {
		fail("child %s: %v", os.Getenv(childName), err)
	}
	none := Range{}
	a, err := Open(Config{
		LeasePath: leasePath,
		Range:     rng,
		Ephemeral: &none,
		Probe:     func(int) bool { return false },
	})
	if err != nil {
		fail("child %s: Open(%s, %s): %v", os.Getenv(childName), leasePath, rng, err)
	}

	if dir := os.Getenv(childBarrier); dir != "" {
		me := os.Getenv(childName)
		if err := os.WriteFile(filepath.Join(dir, "ready."+me), []byte(me), 0o600); err != nil {
			fail("child %s: signalling ready: %v", me, err)
		}
		if !waitForFile(filepath.Join(dir, "go"), 30*time.Second) {
			fail("child %s: the parent never released the barrier", me)
		}
	}

	lease, err := a.Allocate(Request{Cluster: os.Getenv(childName), Account: "did:" + os.Getenv(childName), Instance: "1", Purpose: PurposeKine})
	if err != nil {
		fail("child %s: Allocate: %v", os.Getenv(childName), err)
	}
	fmt.Printf("%s%d\n", childPortLine, lease.Port)
}

func parseChildRange(raw string) (Range, error) {
	first, last, ok := strings.Cut(raw, "-")
	if !ok {
		return Range{}, fmt.Errorf("range %q is not FIRST-LAST", raw)
	}
	a, err1 := strconv.Atoi(first)
	b, err2 := strconv.Atoi(last)
	if err1 != nil || err2 != nil {
		return Range{}, fmt.Errorf("range %q is not FIRST-LAST", raw)
	}
	return Range{a, b}, nil
}

type kid struct {
	name  string
	lease string
	rng   Range
}

func runKids(t *testing.T, kids []kid) []int {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmds := make([]*exec.Cmd, 0, len(kids))
	outs := make([]*bytes.Buffer, 0, len(kids))
	for _, k := range kids {
		var out bytes.Buffer
		cmd := exec.Command(self)
		cmd.Env = append(os.Environ(),
			childSentinel+"=1",
			childName+"="+k.name,
			childLease+"="+k.lease,
			childRange+"="+k.rng.String(),
			childBarrier+"="+dir,
		)
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting child %s: %v", k.name, err)
		}
		cmds = append(cmds, cmd)
		outs = append(outs, &out)
	}
	for _, k := range kids {
		if !waitForFile(filepath.Join(dir, "ready."+k.name), 30*time.Second) {
			t.Fatalf("child %s never loaded its store", k.name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("go"), 0o600); err != nil {
		t.Fatalf("releasing the barrier: %v", err)
	}

	ports := make([]int, 0, len(kids))
	for i, cmd := range cmds {
		err := cmd.Wait()
		if err != nil {
			t.Fatalf("child %s exited %v:\n%s", kids[i].name, err, outs[i].String())
		}
		port, ok := childPort(outs[i].String())
		if !ok {
			t.Fatalf("child %s printed no port:\n%s", kids[i].name, outs[i].String())
		}
		ports = append(ports, port)
	}
	return ports
}

func childPort(out string) (int, bool) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, childPortLine) {
			continue
		}
		p, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, childPortLine)))
		if err != nil {
			return 0, false
		}
		return p, true
	}
	return 0, false
}

func waitForFile(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestCrossProcessOneStoreTwoProcessesGetDistinctPorts(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "ports.json")
	rng := Range{20000, 29999}

	ports := runKids(t, []kid{
		{"alpha", store, rng},
		{"beta", store, rng},
	})
	if ports[0] == ports[1] {
		t.Fatalf("two processes sharing one lease file both got port %d: findFree is lowest-first, "+
			"so without a re-read under a cross-process lock the second process decides against a "+
			"map loaded before the first wrote", ports[0])
	}

	none := Range{}
	a, err := Open(Config{LeasePath: store, Range: rng, Ephemeral: &none, Probe: func(int) bool { return false }})
	if err != nil {
		t.Fatalf("Open(%s): %v", store, err)
	}
	held := a.Leases()
	if len(held) != 2 {
		t.Fatalf("the shared store holds %d leases after two processes allocated, want 2: %+v. "+
			"A save that was not preceded by a re-read under the lock serialises a stale map over "+
			"the other process's lease, and the erased port then reads free", len(held), held)
	}
	for _, name := range []string{"alpha", "beta"} {
		if _, ok := a.PortFor(name, PurposeKine); !ok {
			t.Fatalf("%s's lease did not survive the other process's write: %+v", name, held)
		}
	}
}

func TestCrossProcessSeparateStoresCollideAndTheCarveIsTheFix(t *testing.T) {
	dir := t.TempDir()
	same := Range{20000, 29999}
	ports := runKids(t, []kid{
		{"alpha", filepath.Join(dir, "alpha.json"), same},
		{"beta", filepath.Join(dir, "beta.json"), same},
	})
	if ports[0] != ports[1] {
		t.Fatalf("two processes with separate empty stores and the same carve got %d and %d; "+
			"this test exists to pin that they ALWAYS collide, so if this ever green-flakes the "+
			"reproduction has stopped reproducing", ports[0], ports[1])
	}
	if ports[0] != 20000 {
		t.Fatalf("the collision is at %d, not at rng.First: findFree's lowest-first order is a "+
			"feature (a cluster's number is stable across a wipe and rebuild) and this is its "+
			"cross-process consequence", ports[0])
	}

	dir2 := t.TempDir()
	carved := runKids(t, []kid{
		{"alpha", filepath.Join(dir2, "alpha.json"), Range{20000, 20999}},
		{"beta", filepath.Join(dir2, "beta.json"), Range{21000, 21999}},
	})
	if carved[0] == carved[1] {
		t.Fatalf("disjoint carves still collided on %d", carved[0])
	}
	if !(Range{20000, 20999}).Contains(carved[0]) || !(Range{21000, 21999}).Contains(carved[1]) {
		t.Fatalf("a carve was not honoured: got %d and %d for 20000-20999 and 21000-21999", carved[0], carved[1])
	}
}
