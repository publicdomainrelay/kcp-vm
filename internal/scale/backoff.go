package scale

import (
	"fmt"
	"time"
)

const DefaultBackoffBase = 15 * time.Second

const DefaultBackoffCap = 5 * time.Minute

type Backoff struct {
	Ends int

	Until time.Time

	Reason string
}

func (o Options) backoffBase() time.Duration {
	if o.BackoffBase <= 0 {
		return DefaultBackoffBase
	}
	return o.BackoffBase
}

func (o Options) backoffCap() time.Duration {
	if o.BackoffCap <= 0 {
		return DefaultBackoffCap
	}
	return o.BackoffCap
}

func BackoffDelay(ends int, opts Options) time.Duration {
	if ends <= 0 {
		return 0
	}
	delay, ceiling := opts.backoffBase(), opts.backoffCap()
	for i := 1; i < ends && delay < ceiling; i++ {
		delay *= 2
	}
	if delay > ceiling {
		return ceiling
	}
	return delay
}

func (b Backoff) Holds(now time.Time) bool {
	return !b.Until.IsZero() && now.Before(b.Until)
}

func (st State) Declined(now time.Time, reason string, opts Options) State {
	ends := st.Backoff.Ends + 1
	st.Backoff = Backoff{Ends: ends, Until: now.Add(BackoffDelay(ends, opts)), Reason: reason}
	return st
}

func (st State) Joined() State {
	st.Backoff = Backoff{}
	return st
}

func backingOff(st State, target int, now time.Time) Decision {
	return Decision{
		Action: None, Nodes: st.Nodes, Intent: true, BackoffUntil: st.Backoff.Until,
		Reason: fmt.Sprintf("intent, and %d nodes are counted for a target of %d, and this cluster "+
			"is backing off market provisions until %s (%s from now): %d market provision(s) in a "+
			"row ended without a node (an RFP no bidder took, or a claim that failed or was lost), and "+
			"asking again at once posts a new public RFP every pass -- %s", st.Nodes, target, st.Backoff.Until.UTC().Format(time.RFC3339),
			st.Backoff.Until.Sub(now).Round(time.Second), st.Backoff.Ends, st.Backoff.Reason),
	}
}
