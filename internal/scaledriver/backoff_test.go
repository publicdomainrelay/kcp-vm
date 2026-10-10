package scaledriver

import (
	"context"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const backoffTick = 5 * time.Second

type marketLoop struct {
	t       *testing.T
	now     time.Time
	start   time.Time
	nodes   *fakeNodes
	d       *Driver
	pending bool
	times   []time.Duration
}

func newMarketLoop(t *testing.T) *marketLoop {
	t.Helper()
	l := &marketLoop{t: t, now: time.Unix(1700000000, 0), nodes: &fakeNodes{}}
	l.start = l.now
	l.d = driver(t, &l.now, l.nodes)
	return l
}

func (l *marketLoop) tick(o scale.Observation) {
	l.t.Helper()
	if l.pending {
		l.d.Declined("liveweb", "no bids arrived for this node")
		l.pending = false
	}
	before := l.nodes.provisions
	if _, err := l.d.Pass(context.Background(), "liveweb", o, 0); err != nil {
		l.t.Fatalf("Pass: %v", err)
	}
	if l.nodes.provisions > before {
		l.times = append(l.times, l.now.Sub(l.start))
		l.pending = true
	}
	l.now = l.now.Add(backoffTick)
}

func (l *marketLoop) runUntil(provisions int, o scale.Observation) {
	l.t.Helper()
	for i := 0; len(l.times) < provisions; i++ {
		if i > 100_000 {
			l.t.Fatalf("only %d provisions in %d passes", len(l.times), i)
		}
		l.tick(o)
	}
}

func gaps(times []time.Duration) []time.Duration {
	out := make([]time.Duration, 0, len(times))
	for i := 1; i < len(times); i++ {
		out = append(out, times[i]-times[i-1])
	}
	return out
}

func TestConsecutiveNoBidsEndsProvisionAtGrowingIntervalsCappedAtTenMinutes(t *testing.T) {
	l := newMarketLoop(t)
	l.runUntil(9, running())
	want := []time.Duration{
		backoffTick + 30*time.Second,
		backoffTick + time.Minute,
		backoffTick + 2*time.Minute,
		backoffTick + 4*time.Minute,
		backoffTick + 8*time.Minute,
		backoffTick + 10*time.Minute,
		backoffTick + 10*time.Minute,
		backoffTick + 10*time.Minute,
	}
	got := gaps(l.times)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the gaps between market provisions were %v, want %v. MEASURED on prod "+
				"2026-10-07 15:43-16:48 UTC: with no bidder answering, a new public market.rfp went "+
				"out about every 50 s per missing node, 49 RFPs and 86 no_bids in one hour, because "+
				"an end for lack of bids was given up and asked again on the next pass", got, want)
		}
	}
}

func TestAJoinedNodeResetsTheBackoff(t *testing.T) {
	l := newMarketLoop(t)
	l.runUntil(4, running())
	if ends := l.d.State("liveweb").Backoff.Ends; ends != 3 {
		t.Fatalf("after three ends the back-off counted %d", ends)
	}
	l.pending = false
	l.d.Joined("liveweb")
	if st := l.d.State("liveweb"); st.Backoff != (scale.Backoff{}) {
		t.Fatalf("a node joined and the back-off is still %+v", st.Backoff)
	}
	from := len(l.times)
	l.pending = true
	l.runUntil(from+1, running())
	if gap := l.times[from] - l.times[from-1]; gap != backoffTick+30*time.Second {
		t.Fatalf("the first end after a node joined waited %s, want the base wait %s: a market "+
			"that has just rented a node is not the market that declined three times before it",
			gap, backoffTick+30*time.Second)
	}
}

func TestIntentGoneResetsTheBackoff(t *testing.T) {
	l := newMarketLoop(t)
	l.runUntil(4, running())
	l.pending = false
	l.tick(quiet())
	if st := l.d.State("liveweb"); st.Backoff != (scale.Backoff{}) {
		t.Fatalf("a pass with no intent left the back-off at %+v", st.Backoff)
	}
	from := len(l.times)
	l.tick(running())
	if len(l.times) != from+1 {
		t.Fatal("intent came back after the back-off was reset and the pass did not provision at once")
	}
	l.runUntil(from+2, running())
	if gap := l.times[from+1] - l.times[from]; gap != backoffTick+30*time.Second {
		t.Fatalf("the first end after the intent came back waited %s, want the base wait %s",
			gap, backoffTick+30*time.Second)
	}
}

func TestADeclinedEndRecordsWhyAndUntilWhen(t *testing.T) {
	now := time.Unix(1700000000, 0)
	d := driver(t, &now, &fakeNodes{})
	b := d.Declined("liveweb", "no bids arrived for this node")
	if b.Ends != 1 || !b.Until.Equal(now.Add(30*time.Second)) || b.Reason != "no bids arrived for this node" {
		t.Fatalf("Declined recorded %+v, want one end, a wait until %s and the door's words", b, now.Add(30*time.Second))
	}
	if d.State("other").Backoff != (scale.Backoff{}) {
		t.Fatal("one cluster's back-off reached another cluster")
	}
}
