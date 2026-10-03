//go:build linux

package clusterprovider

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func purposes(t *testing.T, stack Stack, localStore bool) []portallocator.Purpose {
	t.Helper()
	got, err := PurposesFor(stack, localStore)
	if err != nil {
		t.Fatalf("PurposesFor(%v, %v): %v", stack, localStore, err)
	}
	return got
}

func hasPurpose(ps []portallocator.Purpose, want portallocator.Purpose) bool {
	for _, p := range ps {
		if p == want {
			return true
		}
	}
	return false
}

func TestProvenStackLeasesOnlyWhatItRuns(t *testing.T) {
	got := purposes(t, ProvenStack, true)
	for _, want := range []portallocator.Purpose{
		portallocator.PurposePostgres,
		portallocator.PurposeK3sSecure,
		portallocator.PurposeK3sControllerManager,
		portallocator.PurposeK3sScheduler,
		portallocator.PurposeK3sCloudControllerManager,
	} {
		if !hasPurpose(got, want) {
			t.Fatalf("the proven stack leases %v and holds no %s", got, want)
		}
	}
	for _, unwanted := range []portallocator.Purpose{
		portallocator.PurposeOpenBao,
		portallocator.PurposeKine,
	} {
		if hasPurpose(got, unwanted) {
			t.Fatalf("the proven stack runs no component that binds %s, and leasing it reserves a "+
				"port for a process that never starts: %v", unwanted, got)
		}
	}
}

func TestDesignedStackLeasesItsOwnComponents(t *testing.T) {
	got := purposes(t, DesignedStack, true)
	for _, want := range []portallocator.Purpose{
		portallocator.PurposePostgres,
		portallocator.PurposeOpenBao,
		portallocator.PurposeKine,
		portallocator.PurposeK3sSecure,
	} {
		if !hasPurpose(got, want) {
			t.Fatalf("the designed stack runs a component that binds %s and does not lease it: %v", want, got)
		}
	}
}

func TestAnExternalStoreRemovesTheLocalPostgresLease(t *testing.T) {
	got := purposes(t, ProvenStack, false)
	if hasPurpose(got, portallocator.PurposePostgres) {
		t.Fatalf("no local postgres runs when the store is external, so its port must not be leased: %v", got)
	}
}

func TestAnEmptyStackIsRefused(t *testing.T) {
	if _, err := PurposesFor(Stack{}, true); err == nil {
		t.Fatal("an empty Stack leased no ports and was accepted")
	}
}

func TestAComponentWithNoLeaseIsRefusedRatherThanSkipped(t *testing.T) {
	if _, err := PurposesFor(Stack{procgroup.Component("a-new-component")}, true); !errors.Is(err, ErrNoPurposeForComponent) {
		t.Fatalf("a component added to the Stack without a lease must be refused, not silently "+
			"started on a host-fixed port: %v", err)
	}
}

func TestALeaseIsGrantedOncePerPurpose(t *testing.T) {
	once := purposes(t, Stack{procgroup.ComponentPostgres, procgroup.ComponentK3s}, true)
	twice := purposes(t, Stack{procgroup.ComponentPostgres, procgroup.ComponentPostgres, procgroup.ComponentK3s}, true)
	if len(once) != len(twice) {
		t.Fatalf("a repeated component changed the leased set: %v against %v", once, twice)
	}
	seen := map[portallocator.Purpose]int{}
	for _, p := range once {
		seen[p]++
	}
	for p, n := range seen {
		if n != 1 {
			t.Fatalf("%s is leased %d times, and the second request is a second owner of one port", p, n)
		}
	}
}

func TestTheLeasedPurposesAreExactlyTheOnesTheAllocatorGrants(t *testing.T) {
	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
	})
	if err != nil {
		t.Fatalf("opening the allocator: %v", err)
	}
	for _, p := range purposes(t, ProvenStack, true) {
		if _, err := alloc.Allocate(portallocator.Request{Cluster: "liveweb", Instance: "1", Purpose: p}); err != nil {
			t.Fatalf("allocating %s: %v", p, err)
		}
	}
	if _, held := alloc.PortFor("liveweb", portallocator.PurposeOpenBao); held {
		t.Fatal("openbao is not in the proven stack and its port is held")
	}
	for _, p := range purposes(t, ProvenStack, true) {
		if _, held := alloc.PortFor("liveweb", p); !held {
			t.Fatalf("%s is in the leased set and the allocator holds nothing for it", p)
		}
	}
}
