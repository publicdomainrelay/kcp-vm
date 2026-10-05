package scaledriver

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

type fakeNodes struct {
	provisions int
	teardowns  int
	failWith   error
}

func (f *fakeNodes) Provision(context.Context) error {
	f.provisions++
	return f.failWith
}

func (f *fakeNodes) Teardown(context.Context) error {
	f.teardowns++
	return f.failWith
}

var _ scale.Nodes = (*fakeNodes)(nil)

func running() scale.Observation {
	return scale.Observation{Pods: []corev1.Pod{{Status: corev1.PodStatus{Phase: corev1.PodRunning}}}}
}

func quiet() scale.Observation { return scale.Observation{} }

func driver(t *testing.T, now *time.Time, n *fakeNodes) *Driver {
	t.Helper()
	d, err := New(func(string) (scale.Nodes, error) { return n, nil }, Options{
		Now: func() time.Time { return *now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestIntentWithNoNodeProvisionsOne(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	d := driver(t, &now, n)

	dec, err := d.Pass(context.Background(), "liveweb", running(), 0)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if dec.Action != scale.Provision {
		t.Fatalf("action = %v, want Provision (reason: %s)", dec.Action, dec.Reason)
	}
	if n.provisions != 1 {
		t.Fatalf("Provision calls = %d, want 1 -- the decision was made and NOT performed", n.provisions)
	}
	if n.teardowns != 0 {
		t.Errorf("Teardown calls = %d, want 0", n.teardowns)
	}
}

func TestIntentWithANodeActsOnNothing(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	d := driver(t, &now, n)

	dec, err := d.Pass(context.Background(), "liveweb", running(), 1)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if dec.Action != scale.None {
		t.Fatalf("action = %v, want None (reason: %s)", dec.Action, dec.Reason)
	}
	if n.provisions != 0 || n.teardowns != 0 {
		t.Fatalf("the actuator was touched in the steady state: %d provisions, %d teardowns",
			n.provisions, n.teardowns)
	}
}

func TestTheHoldIsWhatStopsAnImmediateTeardown(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	d := driver(t, &now, n)

	dec, err := d.Pass(context.Background(), "liveweb", quiet(), 1)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if dec.Action != scale.None {
		t.Fatalf("action = %v on the first idle pass, want None -- a node is torn down the "+
			"moment its work finishes without the grace", dec.Action)
	}
	if d.State("liveweb").IdleSince.IsZero() {
		t.Fatal("the idle clock did not start, so the grace can never elapse")
	}

	now = now.Add(time.Minute)
	if dec, err = d.Pass(context.Background(), "liveweb", quiet(), 1); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if dec.Action != scale.None {
		t.Fatalf("action = %v after one minute, want None (hold is %s)", dec.Action, scale.DefaultHold)
	}
	if n.teardowns != 0 {
		t.Fatalf("the node was torn down inside the hold: %d teardowns", n.teardowns)
	}

	now = now.Add(scale.DefaultHold)
	dec, err = d.Pass(context.Background(), "liveweb", quiet(), 1)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if dec.Action != scale.Teardown {
		t.Fatalf("action = %v past the hold, want Teardown (reason: %s)", dec.Action, dec.Reason)
	}
	if n.teardowns != 1 {
		t.Fatalf("Teardown calls = %d, want 1", n.teardowns)
	}
}

func TestAFailedProvisionDoesNotAdvanceTheState(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{failWith: errors.New("no such binary")}
	d := driver(t, &now, n)

	if _, err := d.Pass(context.Background(), "liveweb", running(), 0); err == nil {
		t.Fatal("a failed Provision was reported as success")
	}
	if got := d.State("liveweb").Nodes; got != 0 {
		t.Fatalf("state.Nodes = %d after a FAILED provision, want 0 -- the decision would "+
			"believe a node exists that does not", got)
	}

	n.failWith = nil
	dec, err := d.Pass(context.Background(), "liveweb", running(), 0)
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if dec.Action != scale.Provision {
		t.Fatalf("the retry decided %v, want Provision -- a cluster with intent and no node "+
			"must keep asking", dec.Action)
	}
	if n.provisions != 2 {
		t.Fatalf("Provision calls = %d, want 2 (the failure and the retry)", n.provisions)
	}
}

func TestForgetDropsTheIdleClock(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	d := driver(t, &now, n)

	if _, err := d.Pass(context.Background(), "liveweb", quiet(), 1); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if d.State("liveweb").IdleSince.IsZero() {
		t.Fatal("the idle clock did not start")
	}
	d.Forget("liveweb")
	if !d.State("liveweb").IdleSince.IsZero() {
		t.Fatal("Forget left the idle clock behind, so a recreated cluster inherits an age it " +
			"never earned")
	}
}

func TestANilFactoryIsRefused(t *testing.T) {
	if _, err := New(nil, Options{}); !errors.Is(err, ErrNoFactory) {
		t.Fatalf("New(nil) = %v, want ErrNoFactory", err)
	}
}

func TestRunPassReadsOnceAndActs(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	reads := 0
	d, err := New(func(string) (scale.Nodes, error) { return n, nil }, Options{
		Now: func() time.Time { return now },
		Observe: func(context.Context, string) (scale.Observation, int, error) {
			reads++
			return running(), 0, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dec, err := d.RunPass(context.Background(), "liveweb")
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if dec.Action != scale.Provision {
		t.Fatalf("action = %v, want Provision (reason: %s)", dec.Action, dec.Reason)
	}
	if n.provisions != 1 {
		t.Fatalf("Provision calls = %d, want 1", n.provisions)
	}
	if reads != 1 {
		t.Fatalf("Observe calls = %d, want exactly 1", reads)
	}
}

func TestRunPassRefusesWithNothingToObserve(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	d := driver(t, &now, n)
	if _, err := d.RunPass(context.Background(), "liveweb"); !errors.Is(err, ErrNoObserver) {
		t.Fatalf("RunPass without an Observer = %v, want ErrNoObserver", err)
	}
	if n.provisions != 0 || n.teardowns != 0 {
		t.Fatal("the actuator was touched without an observation")
	}
}

func TestAnObservationFailureDoesNotActuate(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	d, err := New(func(string) (scale.Nodes, error) { return n, nil }, Options{
		Now: func() time.Time { return now },
		Observe: func(context.Context, string) (scale.Observation, int, error) {
			return scale.Observation{}, 0, errors.New("the apiserver refused the LIST")
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.RunPass(context.Background(), "liveweb"); err == nil {
		t.Fatal("an observation failure was reported as a completed pass")
	}
	if n.provisions != 0 || n.teardowns != 0 {
		t.Fatalf("a cluster that could not be READ was acted on: %d provisions, %d teardowns -- "+
			"an unreadable cluster is not an idle one", n.provisions, n.teardowns)
	}
}

func runningPods(n int) scale.Observation {
	o := scale.Observation{Pods: make([]corev1.Pod, n)}
	for i := range o.Pods {
		o.Pods[i] = corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	}
	return o
}

func TestTheDriverBringsTheCountUpToTheTarget(t *testing.T) {
	now := time.Unix(1700000000, 0)
	o := runningPods(17)

	n := &fakeNodes{}
	d, err := New(func(string) (scale.Nodes, error) { return n, nil }, Options{
		Now:   func() time.Time { return now },
		Scale: scale.Options{MaxNodes: 3, PodsPerNode: 8},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dec, err := d.Pass(context.Background(), "liveweb", o, 0)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if dec.Action != scale.Provision || dec.Nodes != 3 {
		t.Fatalf("decided %s/%d, want Provision/3", dec.Action, dec.Nodes)
	}
	if n.provisions != 3 {
		t.Fatalf("Provision calls = %d, want 3: the re-read count must be reconciled UP to the "+
			"decision's target, not one node for the pass", n.provisions)
	}

	n = &fakeNodes{}
	d, err = New(func(string) (scale.Nodes, error) { return n, nil }, Options{
		Now:   func() time.Time { return now },
		Scale: scale.Options{MaxNodes: 3, PodsPerNode: 8},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err = d.Pass(context.Background(), "liveweb", o, 1); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if n.provisions != 2 {
		t.Fatalf("from one re-read node, Provision calls = %d, want 2: only the difference to "+
			"the target is provisioned", n.provisions)
	}
}

func TestAFailedScaleOutStopsAndDoesNotAdvance(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{failWith: errors.New("the guest would not boot")}
	d, err := New(func(string) (scale.Nodes, error) { return n, nil }, Options{
		Now:   func() time.Time { return now },
		Scale: scale.Options{MaxNodes: 3, PodsPerNode: 8},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.Pass(context.Background(), "liveweb", runningPods(17), 0); err == nil {
		t.Fatal("a failed scale-out was reported as a completed pass")
	}
	if got := d.State("liveweb").Nodes; got != 0 {
		t.Fatalf("state.Nodes = %d after a FAILED scale-out, want 0 -- the state advances only on "+
			"success", got)
	}
	if n.provisions != 1 {
		t.Fatalf("Provision calls = %d: the loop kept going after the first failure", n.provisions)
	}
}
