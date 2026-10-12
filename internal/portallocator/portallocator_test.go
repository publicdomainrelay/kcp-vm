//go:build linux

package portallocator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	macosEphemeral = Range{49152, 65535}
	linuxEphemeral = Range{32768, 60999}
)

func testProbe(int) bool { return false }

func withAllocator(t *testing.T, rng Range, fn func(a *Allocator, path string)) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	a, err := Open(Config{LeasePath: path, Range: rng, Ephemeral: &macosEphemeral, Probe: testProbe})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	fn(a, path)
}

func reopen(t *testing.T, path string, rng Range) *Allocator {
	t.Helper()
	a, err := Open(Config{LeasePath: path, Range: rng, Ephemeral: &macosEphemeral, Probe: testProbe})
	if err != nil {
		t.Fatalf("Open(restart): %v", err)
	}
	return a
}

func mustAllocate(t *testing.T, a *Allocator, cluster string, purpose Purpose) Lease {
	t.Helper()
	l, err := a.Allocate(Request{Cluster: cluster, Account: "did:" + cluster, Instance: "1", Purpose: purpose})
	if err != nil {
		t.Fatalf("Allocate(%s/%s): %v", cluster, purpose, err)
	}
	return l
}

func TestK3sSecureReservesBothPortsItBinds(t *testing.T) {
	withAllocator(t, Range{20000, 20010}, func(a *Allocator, _ string) {
		secure := mustAllocate(t, a, "a", PurposeK3sSecure)
		if secure.Width != 2 {
			t.Fatalf("PurposeK3sSecure width = %d, want 2: k3s binds N and N+1, and no flag moves "+
				"N+1 on its own", secure.Width)
		}
		if !secure.Covers(secure.Port + 1) {
			t.Fatalf("lease at %d width %d does not cover %d -- the port k3s derives as its own "+
				"kube-apiserver secure port", secure.Port, secure.Width, secure.Port+1)
		}
		next, err := a.Allocate(Request{Cluster: "b", Purpose: PurposeKine})
		if err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if next.Port == secure.Port+1 {
			t.Fatalf("a second cluster was handed %d, which k3s DOES bind as the first cluster's "+
				"apiserver secure port", next.Port)
		}
	})
}

func TestACarveThatSwallowsAGatewayPortIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, shared := range gatewayShared {
		_, err := Open(Config{
			LeasePath: filepath.Join(dir, "ports.json"),
			Range:     shared,
			Ephemeral: &macosEphemeral,
			Probe:     testProbe,
		})
		var conflict *RangeConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("Open(carve %s) = %v, want a RangeConflictError: that range is a port the "+
				"GATEWAY owns once for the whole host, outside the allocator", shared, err)
		}
	}
}

func TestA_NSimultaneousClustersGetNDistinctPorts(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(a *Allocator, _ string) {
		seen := map[int]string{}
		for i := 0; i < 5; i++ {
			c := fmt.Sprintf("c%d", i)
			l := mustAllocate(t, a, c, PurposeKine)
			if other, dup := seen[l.Port]; dup {
				t.Fatalf("duplicate port handed out: %d to both %s and %s", l.Port, other, c)
			}
			seen[l.Port] = c
		}
	})
}

func TestB_NoTransientOverlapAnAllocatedPortIsNeverReHanded(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(a *Allocator, _ string) {
		first := mustAllocate(t, a, "A", PurposeKine)
		second := mustAllocate(t, a, "B", PurposeKine)
		if second.Port == first.Port {
			t.Fatalf("B took A's unbound port %d: a port is not free because it looks unbound", first.Port)
		}
		mustAllocate(t, a, "C", PurposeKine)
		mustAllocate(t, a, "D", PurposeKine)
		leases := a.Leases()
		if len(leases) != 4 {
			t.Fatalf("expected 4 leases, got %d", len(leases))
		}
		ports := map[int]bool{}
		for _, l := range leases {
			if ports[l.Port] {
				t.Fatalf("duplicate port in the store: %d", l.Port)
			}
			ports[l.Port] = true
		}
	})
}

func TestB2_ConcurrentAllocateIsAtomic(t *testing.T) {
	withAllocator(t, Range{20000, 20019}, func(a *Allocator, _ string) {
		const k = 8
		var wg sync.WaitGroup
		got := make([]int, k)
		errs := make([]error, k)
		for i := 0; i < k; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				l, err := a.Allocate(Request{Cluster: fmt.Sprintf("r%d", i), Instance: "1", Purpose: PurposeKine})
				got[i], errs[i] = l.Port, err
			}(i)
		}
		wg.Wait()
		seen := map[int]bool{}
		for i, p := range got {
			if errs[i] != nil {
				t.Fatalf("goroutine %d: %v", i, errs[i])
			}
			if seen[p] {
				t.Fatalf("read-then-write without a lock: two racing allocators got %d (all: %v)", p, got)
			}
			seen[p] = true
		}
	})
}

func TestC_CrashBetweenAllocateAndBindDoesNotFreeThePort(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(_ *Allocator, path string) {
		crashed := reopen(t, path, Range{20000, 20005})
		lease := mustAllocate(t, crashed, "A", PurposeKine)

		restarted := reopen(t, path, Range{20000, 20005})
		held, ok := restarted.PortFor("A", PurposeKine)
		if !ok || held.Port != lease.Port {
			t.Fatalf("port was freed by a crash: a leak here would be recoverable, but only if it is still visible. got %v %v", held, ok)
		}
		next := mustAllocate(t, restarted, "B", PurposeKine)
		if next.Port == lease.Port {
			t.Fatalf("a crashed cluster's port %d was handed to another cluster", lease.Port)
		}
	})
}

func TestC2_ReleaseDoesReleaseAndOnlyThen(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(a *Allocator, _ string) {
		lease := mustAllocate(t, a, "A", PurposeKine)
		if err := a.Release("A"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, ok := a.PortFor("A", PurposeKine); ok {
			t.Fatal("release did not free the lease")
		}
		reused := mustAllocate(t, a, "B", PurposeKine)
		if reused.Port != lease.Port {
			t.Fatalf("released port %d was not returned to the pool; got %d", lease.Port, reused.Port)
		}
	})
}

func TestD_LeakIsBoundedLeaseWithNoCRIsReclaimed(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(a *Allocator, _ string) {
		mustAllocate(t, a, "ghost", PurposeKine)
		if len(a.Leases()) != 1 {
			t.Fatalf("expected 1 lease, got %d", len(a.Leases()))
		}
		freed, err := a.Reclaim(map[string]bool{})
		if err != nil {
			t.Fatalf("Reclaim: %v", err)
		}
		if len(freed) != 1 || freed[0].Cluster != "ghost" {
			t.Fatalf("expected ghost reclaimed, got %v", freed)
		}
		if len(a.Leases()) != 0 {
			t.Fatalf("reclaim did not free the store: %v", a.Leases())
		}
	})
}

func TestD2_ReclaimGraceProtectsTheAllocateThenWriteTheCRRace(t *testing.T) {
	now := time.Now()
	fresh := Lease{Port: 20000, Width: 1, Cluster: "just-made", AllocatedAt: now}
	old := Lease{Port: 20001, Width: 1, Cluster: "ghost", AllocatedAt: now.Add(-time.Hour)}
	freed := LeasesToReclaim([]Lease{fresh, old}, map[string]bool{}, now, 30*time.Second)
	if len(freed) != 1 || freed[0].Cluster != "ghost" {
		t.Fatalf("grace window must spare a lease younger than it and reclaim an older one, got %v", freed)
	}
}

func TestG_OffDoesNotRelease(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(a *Allocator, _ string) {
		lease := mustAllocate(t, a, "A", PurposeKine)

		freed, err := a.Reclaim(map[string]bool{"A": true})
		if err != nil {
			t.Fatalf("Reclaim: %v", err)
		}
		if len(freed) != 0 {
			t.Fatalf("OFF released the port (%v); this cluster's next ON would be a silent misroute", freed)
		}
		if held, ok := a.PortFor("A", PurposeKine); !ok || held.Port != lease.Port {
			t.Fatalf("OFF lost the lease: %v %v", held, ok)
		}

		freed, _ = a.Reclaim(map[string]bool{"A": true})
		if len(freed) != 0 {
			t.Fatalf("a dead instance freed a port that its CR still owns: %v", freed)
		}
	})
}

func TestK_OnAfterOffRebindsTheSamePort(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(a *Allocator, _ string) {
		first := mustAllocate(t, a, "A", PurposeKine)
		second, err := a.Allocate(Request{Cluster: "A", Account: "did:a", Instance: "2", Purpose: PurposeKine})
		if err != nil {
			t.Fatalf("re-bind: %v", err)
		}
		if second.Port != first.Port {
			t.Fatalf("ON after OFF moved the port %d -> %d; status.URL would now point at another cluster", first.Port, second.Port)
		}
		if second.Instance != "2" {
			t.Fatalf("instance was not bumped on re-bind: %q", second.Instance)
		}
		if len(a.Leases()) != 1 {
			t.Fatalf("re-bind allocated a second lease: %v", a.Leases())
		}
	})
}

func TestE_EphemeralOverlapIsAHardStartFailure(t *testing.T) {
	if err := AssertRangeDisjoint(Range{20000, 29999}, macosEphemeral); err != nil {
		t.Fatalf("the default carve must be disjoint from macOS's measured range: %v", err)
	}
	_, err := Open(Config{
		LeasePath: filepath.Join(t.TempDir(), "never-written.json"),
		Range:     Range{49000, 50000},
		Ephemeral: &macosEphemeral,
	})
	var conflict *RangeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("an overlapping carve must fail at START, not narrow silently; got %v", err)
	}
	if !strings.Contains(err.Error(), "intersects the OS ephemeral range") {
		t.Fatalf("the failure must say what it collided with: %v", err)
	}
	if conflict.Range != (Range{49000, 50000}) {
		t.Fatalf("the failure must carry the carve it refused: %v", conflict.Range)
	}
}

func TestE2_DefaultRangeIsDisjointFromBothMeasuredEphemeralRanges(t *testing.T) {
	def := Range{DefaultFirst, DefaultLast}
	for _, eph := range []Range{macosEphemeral, linuxEphemeral} {
		if err := AssertRangeDisjoint(def, eph); err != nil {
			t.Fatalf("default carve %s vs %s: %v", def, eph, err)
		}
	}
	if def.Contains(53812) {
		t.Fatal("the default carve overlaps the host's own ephemeral range")
	}
}

func TestE3_TheCarveMayNotSwallowAGatewaySharedPort(t *testing.T) {
	_, err := Open(Config{
		LeasePath: filepath.Join(t.TempDir(), "never-written.json"),
		Range:     Range{6000, 30000},
		Ephemeral: &macosEphemeral,
		Probe:     testProbe,
	})
	var conflict *RangeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("a carve reaching kcp's 6443 must fail at START; got %v", err)
	}
	if !strings.Contains(err.Error(), "6443") {
		t.Fatalf("the failure must name the port it swallowed: %v", err)
	}
}

func TestF_TheAuditNamesTheRealProblemOnAPlantedCollision(t *testing.T) {
	planted := []Lease{
		{Port: 16443, Width: 1, Purpose: PurposeKine, Cluster: "root:a:web"},
		{Port: 16443, Width: 1, Purpose: PurposeKine, Cluster: "root:b:web"},
	}
	found := FindDuplicatePorts(planted)
	if len(found) != 1 {
		t.Fatalf("the collision went undetected -- the audit is a no-op; got %v", found)
	}
	if !strings.Contains(found[0], "16443") {
		t.Fatalf("must name the port: %s", found[0])
	}
	if !strings.Contains(found[0], "x509") {
		t.Fatalf("must name the SYMPTOM the operator will actually see, so the next person does not chase a certificate: %s", found[0])
	}
	if !strings.Contains(found[0], "ANOTHER CLUSTER'S") {
		t.Fatalf("must name the real fault, not the symptom: %s", found[0])
	}
	if got := FindDuplicatePorts(planted[:1]); len(got) != 0 {
		t.Fatalf("a single lease is not a collision: %v", got)
	}
}

func TestF3_TheAuditFiresAtAllocateWhenTheStoreIsAlreadyWrong(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	writeStore(t, path, []Lease{
		{Port: 20000, Width: 1, Purpose: PurposeKine, Cluster: "a"},
		{Port: 20000, Width: 1, Purpose: PurposeKine, Cluster: "b"},
	})
	a := reopen(t, path, Range{20000, 20005})
	_, err := a.Allocate(Request{Cluster: "c", Instance: "1", Purpose: PurposeKine})
	if err == nil {
		t.Fatal("the audit passed over a store that already holds a duplicate; the check cannot fail, so it is not a check")
	}
	if !strings.Contains(err.Error(), "x509") || !strings.Contains(err.Error(), "ANOTHER CLUSTER'S") {
		t.Fatalf("allocate must refuse with the named fault, not a port conflict: %v", err)
	}
}

func TestF2_TheAgentsOwnDialLineMustMatchThisClustersLease(t *testing.T) {
	line := `time="..." level=info msg="Remotedialer connected to proxy url=\"wss://127.0.0.1:17443/...`
	if err := AssertAgentDialledOurPort(line, Endpoint{LoopbackAdvertiseAddress, 17443}); err != nil {
		t.Fatalf("a matching dial line must pass: %v", err)
	}

	err := AssertAgentDialledOurPort(line, Endpoint{LoopbackAdvertiseAddress, 17444})
	if err == nil || !strings.Contains(err.Error(), "ADVERTISE MISMATCH") {
		t.Fatalf("a mismatched port must fail loudly; got %v", err)
	}
	if !strings.Contains(err.Error(), "x509") {
		t.Fatalf("must name the symptom the operator will see: %v", err)
	}

	if err := AssertAgentDialledOurPort("no dial line here", Endpoint{LoopbackAdvertiseAddress, 17443}); err == nil {
		t.Fatal("a missing dial line passed; a silent log is how this bug hides")
	}
}

func TestH_TheAdvertiseAddressMustBeOneThisHostCarriesAndTheInvariantIsTiedToOneLease(t *testing.T) {
	if err := AssertAdvertiseAddress(LoopbackAdvertiseAddress); err != nil {
		t.Fatalf("the local-process substrate's advertise address must pass: %v", err)
	}
	for _, bad := range []string{"192.168.5.2", "0.0.0.0", "", "localhost", "example.com", "203.0.113.7"} {
		err := AssertAdvertiseAddress(bad)
		if err == nil {
			t.Fatalf("%q was accepted as an advertise address; a node is a local process and the "+
				"only address it can certainly dial is loopback", bad)
		}
		if !strings.Contains(err.Error(), "this host") && !strings.Contains(err.Error(), "IP address") {
			t.Fatalf("the refusal must name what an advertise address has to be, and that a name is "+
				"not one: %v", err)
		}
	}

	lease := Lease{Port: 20000, Width: 1, Purpose: PurposeK3sSecure, Cluster: "A"}
	if err := AssertAdvertiseInvariant(LoopbackAdvertiseAddress, 20000, 20000, lease); err != nil {
		t.Fatalf("forward and advertise-port from ONE lease must pass: %v", err)
	}
	if err := AssertAdvertiseInvariant(LoopbackAdvertiseAddress, 20001, 20001, lease); err == nil {
		t.Fatal("an --advertise-port that is not leased to this cluster must refuse to start")
	}
	if err := AssertAdvertiseInvariant(LoopbackAdvertiseAddress, 20000, 20001, lease); err == nil {
		t.Fatal("a forward disagreeing with the lease must refuse to start")
	}
	if err := AssertAdvertiseInvariant("192.168.5.2", 20000, 20000, lease); err == nil {
		t.Fatal("the invariant carried an address this host does not carry through to the port checks")
	}
	if err := AssertAdvertiseInvariant(LoopbackAdvertiseAddress, 20000, 20000,
		Lease{Port: 20000, Width: 1, Purpose: PurposeKine, Cluster: "A"}); err == nil {
		t.Fatal("a lease for a port nothing advertises was accepted as the port the apiserver is " +
			"advertised on: the advertised port is the port k3s binds its listener on, so any other " +
			"lease names a port no client in the cluster can dial")
	}
	if err := AssertAdvertiseInvariant(LoopbackAdvertiseAddress, 20000, 20000,
		Lease{Port: 20000, Width: 1, Purpose: "advertise", Cluster: "A"}); err == nil {
		t.Fatal("the retired advertise lease was accepted as the apiserver's own port: a second " +
			"lease for the advertisement is the defect this invariant exists to refuse")
	}
	for _, own := range hostAddresses() {
		if err := AssertAdvertiseAddress(own.String()); err != nil {
			t.Fatalf("this host's own address %s was refused: %v -- a node in a guest dials an "+
				"address the guest can route to, and refusing the host's own addresses would make "+
				"the VM substrate unexpressible", own, err)
		}
	}
}

func TestI_ExhaustionFailsLoudlyRatherThanWrappingOrReusing(t *testing.T) {
	withAllocator(t, Range{20000, 20001}, func(a *Allocator, _ string) {
		mustAllocate(t, a, "A", PurposeKine)
		mustAllocate(t, a, "B", PurposeKine)
		_, err := a.Allocate(Request{Cluster: "C", Instance: "1", Purpose: PurposeKine})
		var exhausted *ExhaustedError
		if !errors.As(err, &exhausted) {
			t.Fatalf("expected exhaustion, got %v", err)
		}
	})
}

func TestJ_PostgresAndOpenBaoGetTheirOwnBlocks(t *testing.T) {
	withAllocator(t, Range{20000, 20005}, func(a *Allocator, _ string) {
		pgA := mustAllocate(t, a, "A", PurposePostgres)
		pgB := mustAllocate(t, a, "B", PurposePostgres)
		if pgA.Port == pgB.Port {
			t.Fatalf("two clusters got the same postgres port %d: their DSNs are identical, so B reads A's data", pgA.Port)
		}
		baoA := mustAllocate(t, a, "A", PurposeOpenBao)
		if baoA.Width != 1 {
			t.Fatalf("OpenBao must reserve ONE port, got width %d -- disable_clustering = true "+
				"removes the cluster listener, so the api port is the only one anything binds "+
				"and a second leased port is a port a node can never dial", baoA.Width)
		}
		baoB := mustAllocate(t, a, "B", PurposeOpenBao)
		for p := baoA.Port; p < baoA.Port+baoA.Width; p++ {
			if baoB.Covers(p) {
				t.Fatalf("OpenBao blocks overlap at %d", p)
			}
		}
	})
}

func TestL_TheBindProbeIsAFilterAndNeverAFreeing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	probe := func(p int) bool { return p == 20000 }
	a, err := Open(Config{LeasePath: path, Range: Range{20000, 20002}, Ephemeral: &macosEphemeral, Probe: probe})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l := mustAllocate(t, a, "A", PurposeKine)
	if l.Port != 20001 {
		t.Fatalf("the probe is a secondary filter: a bound-looking port must be skipped, got %d", l.Port)
	}
	b, err := Open(Config{LeasePath: path, Range: Range{20000, 20002}, Ephemeral: &macosEphemeral,
		Probe: func(int) bool { return true }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if held, ok := b.PortFor("A", PurposeKine); !ok || held.Port != 20001 {
		t.Fatalf("a probe verdict dropped a live lease: %v %v", held, ok)
	}
}

func writeStore(t *testing.T, path string, leases []Lease) {
	t.Helper()
	a := &Allocator{path: path, leases: map[leaseKey]Lease{}, now: time.Now}
	for _, l := range leases {
		a.leases[leaseKey{l.AppInstance, l.Cluster, l.Purpose}] = l
	}
	if err := a.save(); err != nil {
		t.Fatalf("writeStore: %v", err)
	}
}

func TestM_TheRetiredPurposesAreNotPurposesAnyMore(t *testing.T) {
	retired := []Purpose{"ssh-server", "ssh-node", "advertise", "kubelet"}
	for _, p := range retired {
		if p.Width() != 0 {
			t.Fatalf("%s still has width %d; a retired purpose must not be allocatable, "+
				"because nothing binds it and the lease would be held forever", p, p.Width())
		}
	}
	withAllocator(t, Range{20000, 20010}, func(a *Allocator, _ string) {
		for _, p := range retired {
			if _, err := a.Allocate(Request{Cluster: "A", Purpose: p}); err == nil {
				t.Fatalf("Allocate(%s) succeeded; a retired purpose must be refused", p)
			}
		}
	})
}

func TestM2_AnOldLeaseFileStillOpensAndLosesOnlyTheRetiredLeases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	now := time.Now()
	writeStore(t, path, []Lease{
		{Port: 20000, Width: 1, Purpose: PurposeKine, Cluster: "A", AllocatedAt: now},
		{Port: 20001, Width: 1, Purpose: "kubelet", Cluster: "A", AllocatedAt: now},
		{Port: 20002, Width: 1, Purpose: "ssh-node", Cluster: "A", AllocatedAt: now},
		{Port: 20003, Width: 2, Purpose: PurposeK3sSecure, Cluster: "A", AllocatedAt: now},
		{Port: 20005, Width: 1, Purpose: "ssh-server", Cluster: "A", AllocatedAt: now},
		{Port: 20006, Width: 1, Purpose: "advertise", Cluster: "A", AllocatedAt: now},
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	if !strings.Contains(string(raw), "ssh-server") || !strings.Contains(string(raw), "advertise") ||
		!strings.Contains(string(raw), "kubelet") {
		t.Fatal("the fixture is not an OLD lease file: it carries no lease for a purpose this " +
			"build has retired, so this test would pass against a build that never retired anything")
	}

	a := reopen(t, path, Range{20000, 20099})
	if l, ok := a.PortFor("A", PurposeKine); !ok || l.Port != 20000 {
		t.Fatalf("the kine lease did not survive: the migration must drop the retired purposes "+
			"and NOTHING else, or a live cluster's component loses the port it binds on its next "+
			"start and binds one another cluster was handed in the meantime: %+v %v", l, ok)
	}
	if l, ok := a.PortFor("A", PurposeK3sSecure); !ok || l.Port != 20003 || l.Width != 2 {
		t.Fatalf("the k3s-secure lease did not survive intact: %+v %v", l, ok)
	}
	for _, l := range a.Leases() {
		if retiredPurposes[l.Purpose] {
			t.Fatalf("a retired lease is still live: %+v", l)
		}
	}

	if l := mustAllocate(t, a, "B", PurposeKine); l.Port != 20001 {
		t.Fatalf("B got %d, want the lowest free port 20001, which is the port the kubelet lease "+
			"held: the retired leases' ports must return to the carve, not stay reserved by a purpose "+
			"nothing can bind", l.Port)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading the lease file: %v", err)
	}
	if strings.Contains(string(after), "ssh-server") || strings.Contains(string(after), "advertise") ||
		strings.Contains(string(after), "kubelet") {
		t.Fatalf("the retired leases are still in the file after a save:\n%s", after)
	}
}

var ipv6Only = net.ListenConfig{Control: setsockopt(syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)}

func setsockopt(level, option, value int) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var inner error
		err := c.Control(func(fd uintptr) {
			inner = syscall.SetsockoptInt(int(fd), level, option, value)
		})
		if err != nil {
			return err
		}
		return inner
	}
}

func holdListener(network, address string) func(t *testing.T) (int, func()) {
	return func(t *testing.T) (int, func()) {
		t.Helper()
		l, err := net.Listen(network, address)
		if err != nil {
			t.Fatalf("the holder could not bind %s: %v", address, err)
		}
		return l.Addr().(*net.TCPAddr).Port, func() { l.Close() }
	}
}

func holdV6Only(address string) func(t *testing.T) (int, func()) {
	return func(t *testing.T) (int, func()) {
		t.Helper()
		l, err := ipv6Only.Listen(context.Background(), "tcp6", address)
		if err != nil {
			t.Fatalf("the holder could not bind %s ipv6-only: %v", address, err)
		}
		return l.Addr().(*net.TCPAddr).Port, func() { l.Close() }
	}
}

func holdReserved(t *testing.T) (int, func()) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("the holder could not open a socket: %v", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fd)
		t.Fatalf("the holder could not set SO_REUSEADDR: %v", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{}); err != nil {
		syscall.Close(fd)
		t.Fatalf("the holder could not bind the wildcard: %v", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		syscall.Close(fd)
		t.Fatalf("the holder could not read back the port it bound: %v", err)
	}
	bound, ok := sa.(*syscall.SockaddrInet4)
	if !ok {
		syscall.Close(fd)
		t.Fatalf("the holder bound %T, not an ipv4 address", sa)
	}
	return bound.Port, func() { syscall.Close(fd) }
}

type portHolder struct {
	name      string
	linuxOnly bool
	hold      func(t *testing.T) (int, func())
}

func TestN_AHeldPortIsHeldWhicheverAddressTheHolderBound(t *testing.T) {
	cases := []portHolder{
		{"the holder bound the wildcard", false, holdListener("tcp", "0.0.0.0:0")},
		{"the holder bound loopback", false, holdListener("tcp", "127.0.0.1:0")},
		{"the holder bound a DIFFERENT loopback address", false, holdListener("tcp", "127.0.0.2:0")},
		{"the holder reserved the port with SO_REUSEADDR and has not listened yet", false, holdReserved},
		{"the holder bound the ipv6 wildcard ipv6-only", true, holdV6Only("[::]:0")},
	}
	for _, tc := range cases {
		if tc.linuxOnly && runtime.GOOS != "linux" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			port, release := tc.hold(t)
			defer release()
			if !probeBound(port) {
				t.Fatalf("probeBound(%d) = false while %s. The allocator would lease this port and the "+
					"cluster that took the lease can never bind it. A probe that binds ONE address, or that "+
					"keeps Go's default SO_REUSEADDR, coexists with the holder and reads that coexistence "+
					"as freedom -- hold is not a property of the address we chose to try", port, tc.name)
			}
		})
	}
}

func TestN2_APortHeldOnAnotherAddressIsNeverHandedToACluster(t *testing.T) {
	held, release := holdListener("tcp", "127.0.0.2:0")(t)
	defer release()

	none := Range{}
	a, err := Open(Config{
		LeasePath: filepath.Join(t.TempDir(), "ports.json"),
		Range:     Range{held, held + 4},
		Ephemeral: &none,
	})
	if err != nil {
		t.Fatalf("Open over the held port's own range: %v", err)
	}
	l := mustAllocate(t, a, "A", PurposeKine)
	if l.Port == held {
		t.Fatalf("the allocator leased %d, which is already bound. The forward this lease authorises "+
			"cannot bind, so the cluster comes up routed nowhere -- which is the whole failure this "+
			"package exists to prevent", held)
	}
	if !a.Range().Contains(l.Port) {
		t.Fatalf("the allocator skipped to %d, outside the carve %s", l.Port, a.Range())
	}
}

func TestE4_TheZeroRangeMeansTheDocumentedDefault(t *testing.T) {
	if !(Range{}).Empty() {
		t.Fatal("the zero Range is not Empty, so Config.Range's default is unreachable")
	}
	a, err := Open(Config{
		LeasePath: filepath.Join(t.TempDir(), "first-boot.json"),
		Ephemeral: &macosEphemeral,
		Probe:     testProbe,
	})
	if err != nil {
		t.Fatalf("Open with no Range: %v", err)
	}
	if got, want := a.Range(), (Range{DefaultFirst, DefaultLast}); got != want {
		t.Fatalf("Range() = %s, want the documented default %s", got, want)
	}
	a2, err := Open(Config{
		LeasePath: filepath.Join(t.TempDir(), "carved.json"),
		Range:     Range{20000, 20005},
		Ephemeral: &macosEphemeral,
		Probe:     testProbe,
	})
	if err != nil {
		t.Fatalf("Open with an explicit Range: %v", err)
	}
	if got := a2.Range(); got != (Range{20000, 20005}) {
		t.Fatalf("Range() = %s, want the supplied carve", got)
	}
}

func TestTheMarketAdvertiseAddressIsOneThisHostDoesNotCarryAndIsNotLoopback(t *testing.T) {
	if err := AssertMarketAdvertiseAddress(MarketAdvertiseAddress); err != nil {
		t.Fatalf("the market substrate's own constant must pass: %v", err)
	}
	for _, bad := range []string{
		"127.0.0.1", "::1", "0.0.0.0", "", "node.example.invalid", "100.127.255.999",
	} {
		err := AssertMarketAdvertiseAddress(bad)
		if err == nil {
			t.Fatalf("%q was accepted as the address a market node dials its apiserver on. The "+
				"guest puts that address on its OWN loopback and the door reverse-forwards it to "+
				"this host, so a loopback or a wildcard names nothing to put anywhere and a name "+
				"resolves differently in every guest", bad)
		}
	}
	for _, own := range hostAddresses() {
		if own.IsUnspecified() || own.IsLoopback() {
			continue
		}
		err := AssertMarketAdvertiseAddress(own.String())
		if err == nil {
			t.Fatalf("%q is an address this host already carries, and it was accepted: the "+
				"forward the door opens would collide with whatever answers it here", own)
		}
	}

	lease := Lease{Port: 20000, Width: 1, Purpose: PurposeK3sSecure, Cluster: "A"}
	if err := AssertMarketAdvertiseInvariant(MarketAdvertiseAddress, 20000, 20000, lease); err != nil {
		t.Fatalf("a market placement from one lease must pass: %v", err)
	}
	if err := AssertMarketAdvertiseInvariant(LoopbackAdvertiseAddress, 20000, 20000, lease); err == nil {
		t.Fatal("the market invariant accepted the local-process advertise address: k3s refuses a " +
			"loopback address for the in-cluster `kubernetes` Service endpoint, so such a cluster " +
			"fails to start")
	}
	if err := AssertMarketAdvertiseInvariant(MarketAdvertiseAddress, 20001, 20001, lease); err == nil {
		t.Fatal("an --advertise-port that is not leased to this cluster must refuse to start")
	}
}
