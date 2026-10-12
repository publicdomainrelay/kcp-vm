package liveports

import (
	"errors"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

func allSpans() []portallocator.Range {
	out := make([]portallocator.Range, 0, len(Blocks)+len(Probed)+len(Reserved))
	for _, b := range Blocks {
		out = append(out, b.Span())
	}
	for _, s := range Probed {
		out = append(out, s.Span())
	}
	for _, s := range Reserved {
		out = append(out, s.Span())
	}
	return out
}

func claimants() []string {
	out := make([]string, 0, len(Blocks)+len(Probed)+len(Reserved))
	for _, b := range Blocks {
		out = append(out, b.What)
	}
	for _, s := range Probed {
		out = append(out, s.What)
	}
	for _, s := range Reserved {
		out = append(out, s.What)
	}
	return out
}

func TestEveryBlockIsDisjointFromEveryOtherClaimant(t *testing.T) {
	spans, who := allSpans(), claimants()
	for i := range spans {
		for j := i + 1; j < len(spans); j++ {
			if spans[i].Overlaps(spans[j]) {
				t.Fatalf("%s holds %s and %s holds %s. Two live claims that overlap are handed the "+
					"same ports, and the run that loses does not fail to bind -- it fails its readiness "+
					"deadline, which reads as a broken control plane in whatever was committed last. "+
					"Move one of them, or narrow it until they are disjoint: a width is a convenience "+
					"and the disjointness is not", who[i], spans[i], who[j], spans[j])
			}
		}
	}
}

func TestEveryBlockSitsOutsideTheEphemeralRangeAndAboveThePrivilegedOne(t *testing.T) {
	ephemeral, err := portallocator.ReadEphemeralRange()
	if err != nil {
		t.Fatalf("reading the host's ephemeral port range: %v. Every block must sit outside it, "+
			"because a port the kernel hands to an unrelated connection is a bind that fails "+
			"intermittently and for no visible reason", err)
	}
	spans, who := allSpans(), claimants()
	for i, r := range spans {
		if r.First < 1024 {
			t.Fatalf("%s starts at %d, inside the privileged range; every port here is bound by a "+
				"non-root test process", who[i], r.First)
		}
		if r.Last > 65535 {
			t.Fatalf("%s ends at %d, past the last port there is", who[i], r.Last)
		}
		if r.Overlaps(ephemeral) {
			t.Fatalf("%s holds %s and this host's ephemeral range is %s. The kernel hands ports out "+
				"of that range to outgoing connections, so a block inside it is a bind that fails "+
				"only sometimes -- the failure this whole map exists to remove",
				who[i], r, ephemeral)
		}
	}
}

func TestABlocksSlotsTileItAndStayInsideIt(t *testing.T) {
	for _, b := range Blocks {
		span := b.Span()
		for i := 0; i < b.Slots; i++ {
			slot := b.Slot(i)
			if slot.First < span.First || slot.Last > span.Last {
				t.Fatalf("%s slot %d is %s and the block is %s: a slot outside its own block takes "+
					"ports a neighbour holds", b.What, i, slot, span)
			}
			if i > 0 && b.Slot(i-1).Last+1 != slot.First {
				t.Fatalf("%s slot %d starts at %d and slot %d ended at %d: a gap between slots is "+
					"space no slot claims, and a hole in a block is where the next block gets placed "+
					"and then overlapped", b.What, i, slot.First, i-1, b.Slot(i-1).Last)
			}
		}
		if got := b.Slot(b.Slots - 1).Last; got != span.Last {
			t.Fatalf("%s ends at %d and its last slot ends at %d: the slots must tile the block "+
				"exactly, or the map's disjointness says nothing about the ports a run binds",
				b.What, span.Last, got)
		}
	}
}

func TestTwoClaimantsOfOneBlockGetDifferentSlots(t *testing.T) {
	first, err := TakeForProcess(RebuildProof)
	if err != nil {
		t.Fatalf("TakeForProcess(%s): %v", RebuildProof.What, err)
	}
	second, err := TakeForProcess(RebuildProof)
	if err != nil {
		t.Fatalf("TakeForProcess(%s) again: %v. A second claimant in one process must be able to "+
			"take a different slot; if the take were keyed on the process alone, two test binaries "+
			"that both claimed would still share ports", RebuildProof.What, err)
	}
	if first.Overlaps(second) {
		t.Fatalf("two claims on %s both got %s and %s", RebuildProof.What, first, second)
	}
	for _, got := range []portallocator.Range{first, second} {
		if got.First < RebuildProof.Span().First || got.Last > RebuildProof.Span().Last {
			t.Fatalf("a claim on %s returned %s, outside the block %s", RebuildProof.What, got, RebuildProof.Span())
		}
	}
}

func TestABlockWithEverySlotHeldRefuses(t *testing.T) {
	exhausted := Block{What: "the exhausted block", First: 65000, Slots: 2, Width: 5}
	for i := 0; i < exhausted.Slots; i++ {
		if _, err := TakeForProcess(exhausted); err != nil {
			t.Fatalf("claiming slot %d of %s: %v", i, exhausted.What, err)
		}
	}
	if _, err := TakeForProcess(exhausted); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a block with every slot held returned %v, want ErrNoSlot. A take that succeeds "+
			"anyway hands out a slot another process is binding, which is the collision this map "+
			"exists to prevent -- and it would do it silently, only under the concurrency that "+
			"nothing else in this suite reproduces", err)
	}
}

func TestTheDevCarveDoesNotOverlapTheLiveTestBlocks(t *testing.T) {
	dev := DevCarve.Span()
	if dev.First != portallocator.DevFirst || dev.Last != portallocator.DevLast {
		t.Fatalf("liveports.DevCarve is %s and portallocator's dev carve is %d-%d", dev, portallocator.DevFirst, portallocator.DevLast)
	}
	found := false
	for _, s := range Reserved {
		if s == DevCarve {
			found = true
		}
	}
	if !found {
		t.Fatalf("the dev carve is not in Reserved, so a new block could be placed over it")
	}
	for _, b := range Blocks {
		if dev.Overlaps(b.Span()) {
			t.Fatalf("the dev carve %s overlaps %s %s", dev, b.What, b.Span())
		}
	}
	for _, s := range Probed {
		if dev.Overlaps(s.Span()) {
			t.Fatalf("the dev carve %s overlaps %s %s", dev, s.What, s.Span())
		}
	}
	if dev.Overlaps(DefaultCarve.Span()) {
		t.Fatalf("the dev carve %s overlaps the production default carve %s", dev, DefaultCarve.Span())
	}
}
