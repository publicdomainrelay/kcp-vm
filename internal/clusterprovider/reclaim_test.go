//go:build linux

package clusterprovider

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

type emptyList struct{ *fakeClusters }

func (emptyList) Refs(context.Context) ([]Ref, error) { return nil, nil }

func reclaimStore(t *testing.T) *portallocator.Allocator {
	t.Helper()
	a, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "ports.json"),
		Range:     portallocator.Range{First: 20000, Last: 20099},
		Ephemeral: &portallocator.Range{First: 49152, Last: 65535},
		Probe:     func(int) bool { return false },
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	for _, cluster := range []string{key(), "ghost"} {
		if _, err := a.Allocate(portallocator.Request{
			Cluster: cluster, Instance: "1", Purpose: portallocator.PurposeKine,
		}); err != nil {
			t.Fatalf("Allocate(%s): %v", cluster, err)
		}
	}
	return a
}

func runOnePass(t *testing.T, registry Clusters, ports *portallocator.Allocator) {
	t.Helper()
	p, err := New(Options{
		Registry: registry,
		Decider:  decider(t, &fakeHost{}),
		Ports:    ports,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunReclaimsLeasesWithNoClusterCRAndKeepsTheOnesThatHaveOne(t *testing.T) {
	ports := reclaimStore(t)
	runOnePass(t, &fakeClusters{cluster: cluster(false, "", "")}, ports)

	if _, ok := ports.PortFor("ghost", portallocator.PurposeKine); ok {
		t.Fatal("a lease whose Cluster CR is absent survived the reclaim pass: this is the leak " +
			"ADR 0026 says is bounded, and the carve is finite")
	}
	if held, ok := ports.PortFor(key(), portallocator.PurposeKine); !ok {
		t.Fatal("reclaim freed the lease of a cluster whose CR EXISTS. A cluster that is OFF has " +
			"a CR -- spec.running=false is a field, not a deletion -- so this is how an OFF " +
			"cluster loses the lease it will bind on its next ON and binds a port another cluster " +
			"was handed in the meantime")
	} else if held.Port != 20000 {
		t.Fatalf("liveweb's port moved to %d", held.Port)
	}
}

func TestRunDoesNotReclaimOnAnEmptyListing(t *testing.T) {
	ports := reclaimStore(t)
	runOnePass(t, emptyList{&fakeClusters{}}, ports)

	if leases := ports.Leases(); len(leases) != 2 {
		t.Fatalf("an empty listing reclaimed %d leases: a provider that sees no clusters must not "+
			"conclude that every cluster is gone, or one misconfigured kcp tree frees every port "+
			"on the host -- %+v", 2-len(leases), leases)
	}
}

func TestRunWithNoAllocatorLeavesTheStoreAlone(t *testing.T) {
	ports := reclaimStore(t)
	p, err := New(Options{
		Registry: &fakeClusters{cluster: cluster(false, "", "")},
		Decider:  decider(t, &fakeHost{}),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run with no allocator: %v", err)
	}
	if leases := ports.Leases(); len(leases) != 2 {
		t.Fatalf("a provider with no allocator touched a store: %+v", leases)
	}
}

var _ Clusters = emptyList{}
