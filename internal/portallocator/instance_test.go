package portallocator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func openApp(t *testing.T, path, app string, rng Range) *Allocator {
	t.Helper()
	none := Range{}
	a, err := Open(Config{LeasePath: path, AppInstance: app, Range: rng, Ephemeral: &none, Probe: func(int) bool { return false }})
	if err != nil {
		t.Fatalf("Open(%s, %s): %v", path, app, err)
	}
	return a
}

func TestALeaseIsKeyedByInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	rng := Range{DevFirst, DevLast}
	a := openApp(t, path, "worktree-a-1a2b3c4d", rng)
	b := openApp(t, path, "worktree-b-5e6f7a8b", rng)
	la, err := a.Allocate(Request{Cluster: "c1", Purpose: PurposeApiserverLink})
	if err != nil {
		t.Fatal(err)
	}
	lb, err := b.Allocate(Request{Cluster: "c1", Purpose: PurposeApiserverLink})
	if err != nil {
		t.Fatal(err)
	}
	if la.Port == lb.Port {
		t.Fatalf("two application instances leasing cluster c1/%s share port %d", PurposeApiserverLink, la.Port)
	}
	if la.AppInstance != "worktree-a-1a2b3c4d" || lb.AppInstance != "worktree-b-5e6f7a8b" {
		t.Fatalf("a lease does not name its application instance: %+v %+v", la, lb)
	}
	again, err := a.Allocate(Request{Cluster: "c1", Purpose: PurposeApiserverLink})
	if err != nil || again.Port != la.Port {
		t.Fatalf("a re-lease by the same instance moved %d -> %d (%v)", la.Port, again.Port, err)
	}
	if got, ok := b.PortFor("c1", PurposeApiserverLink); !ok || got.Port != lb.Port {
		t.Fatalf("instance b reads %+v %v for its own lease, want %d", got, ok, lb.Port)
	}
	if len(a.Leases()) != 1 || len(b.Leases()) != 1 || len(a.AllLeases()) != 2 {
		t.Fatalf("own a=%v own b=%v all=%v", a.Leases(), b.Leases(), a.AllLeases())
	}
}

func TestOneInstanceNeverReleasesOrReclaimsAnothersLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	rng := Range{DevFirst, DevLast}
	a := openApp(t, path, "inst-a", rng)
	b := openApp(t, path, "inst-b", rng)
	lb, err := b.Allocate(Request{Cluster: "c1", Purpose: PurposeK3sSecure})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Allocate(Request{Cluster: "c1", Purpose: PurposeK3sSecure}); err != nil {
		t.Fatal(err)
	}
	if freed, err := a.Reclaim(map[string]bool{}); err != nil || len(freed) != 1 || freed[0].AppInstance != "inst-a" {
		t.Fatalf("a's reclaim with no known clusters freed %+v (%v); want only a's own lease", freed, err)
	}
	if err := a.Release("c1"); err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.PortFor("c1", PurposeK3sSecure); !ok || got.Port != lb.Port {
		t.Fatalf("instance a's release and reclaim touched instance b's lease: %+v %v", got, ok)
	}
	if moved, err := a.Rekey("c1", "c2"); err != nil || moved != 0 {
		t.Fatalf("a rekeyed %d of b's leases (%v)", moved, err)
	}
	if err := b.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if all := a.AllLeases(); len(all) != 0 {
		t.Fatalf("ReleaseAll left %+v", all)
	}
}

func TestTwoInstancesSharingOneLeaseFileNeverLeaseTheSamePort(t *testing.T) {
	store := filepath.Join(t.TempDir(), "leases.json")
	rng := Range{DevFirst, DevLast}
	ports := runKids(t, []kid{
		{name: "alpha", lease: store, rng: rng, app: "inst-alpha", cluster: "shared"},
		{name: "beta", lease: store, rng: rng, app: "inst-beta", cluster: "shared"},
	})
	if ports[0] == ports[1] {
		t.Fatalf("two application instances in two processes, one lease file, same cluster and purpose, both got %d", ports[0])
	}
	all := openApp(t, store, "", rng).AllLeases()
	if len(all) != 2 {
		t.Fatalf("the shared file holds %d leases, want 2: %+v", len(all), all)
	}
	for _, app := range []string{"inst-alpha", "inst-beta"} {
		if _, ok := openApp(t, store, app, rng).PortFor("shared", PurposeKine); !ok {
			t.Fatalf("%s lost its lease to the other process's write: %+v", app, all)
		}
	}
}

func TestAProdLeaseFileWithoutInstancesReadsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	prod := fmt.Sprintf(`{
  "version": 1,
  "leases": [
    {
      "port": 20000,
      "width": 2,
      "purpose": "k3s-secure",
      "cluster": "prod-a",
      "account": "did:plc:a",
      "instance": "7",
      "allocatedAt": %q
    },
    {
      "port": 20002,
      "width": 1,
      "purpose": "postgres",
      "cluster": "prod-a",
      "allocatedAt": %q
    }
  ]
}
`, at.Format(time.RFC3339), at.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(prod), 0o600); err != nil {
		t.Fatal(err)
	}
	a := openApp(t, path, "", Range{DefaultFirst, DefaultLast})
	got, ok := a.PortFor("prod-a", PurposeK3sSecure)
	if !ok || got.Port != 20000 || got.Instance != "7" || got.AppInstance != "" {
		t.Fatalf("prod lease read as %+v %v", got, ok)
	}
	if len(a.Leases()) != 2 {
		t.Fatalf("a prod allocator sees %d of its 2 leases", len(a.Leases()))
	}
	if _, err := a.Allocate(Request{Cluster: "prod-a", Purpose: PurposePostgres}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != prod {
		t.Fatalf("a prod re-save changed the file:\n%s\nwant\n%s", after, prod)
	}
	if strings.Contains(string(after), "appInstance") {
		t.Fatalf("a prod lease file grew an appInstance key")
	}
	dev := openApp(t, path, "dev-x", Range{DefaultFirst, DefaultLast})
	if _, ok := dev.PortFor("prod-a", PurposeK3sSecure); ok {
		t.Fatalf("an application instance reads the prod lease as its own")
	}
	l, err := dev.Allocate(Request{Cluster: "prod-a", Purpose: PurposeK3sSecure})
	if err != nil {
		t.Fatal(err)
	}
	if l.Port < 20003 {
		t.Fatalf("instance dev-x was handed %d, inside the prod leases 20000-20002", l.Port)
	}
}

func TestFiftyConcurrentLeasesHoldTheFileLockBriefly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	rng := Range{DevFirst, DevLast}
	const n = 50
	waits := make([]time.Duration, n)
	errs := make([]error, n)
	ports := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		a := openApp(t, path, fmt.Sprintf("inst-%02d", i), rng)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			t0 := time.Now()
			l, err := a.Allocate(Request{Cluster: "c", Purpose: PurposeApiserverLink})
			waits[i], errs[i], ports[i] = time.Since(t0), err, l.Port
		}()
	}
	t0 := time.Now()
	close(start)
	wg.Wait()
	total := time.Since(t0)
	seen := map[int]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[ports[i]] {
			t.Fatalf("port %d leased twice", ports[i])
		}
		seen[ports[i]] = true
	}
	sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
	t.Logf("50 concurrent leases, one file: total %s, per-lease wait p50 %s max %s, mean lock hold %s",
		total.Round(time.Microsecond), waits[n/2].Round(time.Microsecond), waits[n-1].Round(time.Microsecond),
		(total / n).Round(time.Microsecond))
	if total > 10*time.Second {
		t.Fatalf("50 leases took %s under one flock", total)
	}
	var f leaseFile
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &f); err != nil || len(f.Leases) != n {
		t.Fatalf("file holds %d leases (%v)", len(f.Leases), err)
	}
}
