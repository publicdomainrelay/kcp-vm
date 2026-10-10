package scale

import (
	"strings"
	"testing"
	"time"
)

func TestTheBackoffDoublesFromItsBaseAndStopsAtItsCap(t *testing.T) {
	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		10 * time.Minute, 10 * time.Minute, 10 * time.Minute,
	}
	for i, w := range want {
		if got := BackoffDelay(i+1, Options{}); got != w {
			t.Errorf("after %d market ends with no contract the wait is %s, want %s: base*2^n from "+
				"a %s base, capped at %s", i+1, got, w, DefaultBackoffBase, DefaultBackoffCap)
		}
	}
	if got := BackoffDelay(10_000, Options{}); got != DefaultBackoffCap {
		t.Fatalf("after ten thousand ends the wait is %s, want the cap %s: a shift that overflows "+
			"reads as a wait of zero, which is the loop this back-off exists to end", got, DefaultBackoffCap)
	}
	if got := BackoffDelay(0, Options{}); got != 0 {
		t.Fatalf("with no end recorded the wait is %s, want zero", got)
	}
	if got := BackoffDelay(3, Options{BackoffBase: time.Second, BackoffCap: 3 * time.Second}); got != 3*time.Second {
		t.Fatalf("a configured base and cap gave %s, want the cap of 3s", got)
	}
}

func TestADeclinedMarketHoldsTheNextProvisionUntilTheWaitIsOver(t *testing.T) {
	o := pods(1)
	st := State{}.Declined(t0, "no bids arrived for this node", Options{})
	held := Decide(o, st, at(29*time.Second), Options{})
	if held.Action != None {
		t.Fatalf("29 s after the first end the decision was %s, want None: the market answered "+
			"with no contract and asking again at once is a new public RFP every pass", held.Action)
	}
	if !held.Intent {
		t.Fatal("a held decision dropped the intent it was holding")
	}
	if !held.BackoffUntil.Equal(at(30 * time.Second)) {
		t.Fatalf("the held decision names %s as the end of the wait, want %s",
			held.BackoffUntil, at(30*time.Second))
	}
	for _, part := range []string{"backing off", at(30 * time.Second).Format(time.RFC3339), "no bids arrived for this node"} {
		if !strings.Contains(held.Reason, part) {
			t.Errorf("the held decision's reason %q does not say %q -- the log line is the only "+
				"place an operator learns that the cluster is waiting, until when and why", held.Reason, part)
		}
	}
	if d := Decide(o, st, at(30*time.Second), Options{}); d.Action != Provision {
		t.Fatalf("at the end of the wait the decision was %s, want Provision", d.Action)
	}
}

func TestTheBackoffSurvivesAProvisionAndDiesWithTheIntent(t *testing.T) {
	st := State{}.Declined(t0, "no bids", Options{})
	provision := Decision{Action: Provision, Nodes: 1, Intent: true}
	if got := st.Next(provision, at(time.Minute)); got.Backoff.Ends != 1 {
		t.Fatalf("a provision dropped the back-off (%d ends, want 1): the next end has to wait "+
			"longer than this one did, so the count of ends outlives the provision it allowed", got.Backoff.Ends)
	}
	held := Decision{Action: None, Nodes: 0, Intent: true}
	if got := st.Next(held, at(time.Second)); got.Backoff != st.Backoff {
		t.Fatalf("a held pass changed the back-off from %+v to %+v", st.Backoff, got.Backoff)
	}
	quiet := Decision{Action: None, Nodes: 0}
	if got := st.Next(quiet, at(time.Second)); got.Backoff != (Backoff{}) {
		t.Fatalf("a pass with no intent kept the back-off %+v: intent that went away resets it, "+
			"so the next workload is not made to wait out the last one's luck", got.Backoff)
	}
	if got := st.Joined(); got.Backoff != (Backoff{}) {
		t.Fatalf("a node that joined kept the back-off %+v", got.Backoff)
	}
}

func TestANodeCountAtTheTargetIsNotReportedAsBackingOff(t *testing.T) {
	st := State{Nodes: 1}.Declined(t0, "no bids", Options{})
	d := Decide(pods(1), st, at(time.Second), Options{})
	if d.Action != None || !d.BackoffUntil.IsZero() {
		t.Fatalf("a cluster at its target decided %s with a wait until %s, want None with no wait: "+
			"there is no provision to hold back", d.Action, d.BackoffUntil)
	}
}
