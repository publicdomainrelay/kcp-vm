package subnetallocator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openApp(t *testing.T, path, app string) *Allocator {
	t.Helper()
	a, err := openWithPool(Config{LeasePath: path, AppInstance: app, HostNetworks: fakeHost}, tinyPool())
	if err != nil {
		t.Fatalf("openWithPool(%s): %v", app, err)
	}
	return a
}

func TestASubnetLeaseIsKeyedByInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "networks.json")
	a := openApp(t, path, "inst-a")
	b := openApp(t, path, "inst-b")
	la := mustAllocate(t, a, "c1")
	lb := mustAllocate(t, b, "c1")
	if la.Block.Masked() == lb.Block.Masked() {
		t.Fatalf("two application instances' cluster c1 share %s", la.Block)
	}
	if la.AppInstance != "inst-a" || lb.AppInstance != "inst-b" {
		t.Fatalf("a lease does not name its application instance: %+v %+v", la, lb)
	}
	if err := a.Release("c1"); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.LeaseFor("c1"); !ok || got.Block != lb.Block {
		t.Fatalf("instance a's release touched instance b's lease: %+v %v", got, ok)
	}
	if _, ok := a.LeaseFor("c1"); ok {
		t.Fatalf("instance a still reads its released lease")
	}
	if moved, err := a.Rekey("c1", "c2"); err != nil || moved != 0 {
		t.Fatalf("instance a rekeyed instance b's lease: %d %v", moved, err)
	}
	if len(b.Leases()) != 1 || len(a.Leases()) != 0 || len(a.AllLeases()) != 1 {
		t.Fatalf("own a=%v own b=%v all=%v", a.Leases(), b.Leases(), a.AllLeases())
	}
}

func TestAProdSubnetLeaseFileWithoutInstancesReadsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "networks.json")
	prod := `{
  "version": 1,
  "pool": "10.200.0.0/26",
  "leases": [
    {
      "cluster": "prod-a",
      "block": "10.200.0.0/29",
      "allocatedAt": "2026-10-01T00:00:00Z"
    }
  ]
}
`
	if err := os.WriteFile(path, []byte(prod), 0o600); err != nil {
		t.Fatal(err)
	}
	a := openApp(t, path, "")
	if l, ok := a.LeaseFor("prod-a"); !ok || l.Block.String() != "10.200.0.0/29" || l.AppInstance != "" {
		t.Fatalf("prod lease read as %+v %v", l, ok)
	}
	if _, err := a.Allocate(Request{Cluster: "prod-a"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "appInstance") {
		t.Fatalf("a prod subnet lease file grew an appInstance key:\n%s", after)
	}
	dev := openApp(t, path, "dev-x")
	if _, ok := dev.LeaseFor("prod-a"); ok {
		t.Fatalf("an application instance reads the prod lease as its own")
	}
	l := mustAllocate(t, dev, "prod-a")
	if l.Block.String() == "10.200.0.0/29" {
		t.Fatalf("instance dev-x was handed the prod block")
	}
}
