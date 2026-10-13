package scale

import (
	"testing"
	"time"
)

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
