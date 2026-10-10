package scaledriver

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

type releasingNodes struct {
	fakeNodes
	released []string
}

func (r *releasingNodes) Release(_ context.Context, node string) error {
	r.released = append(r.released, node)
	return r.failWith
}

func twoIdleNodesForOne() scale.Observation {
	o := scale.Observation{Pods: []corev1.Pod{{
		Spec:   corev1.PodSpec{NodeName: "c-node1"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}}}
	o.Placement = scale.Placement{Nodes: []scale.NodeLoad{
		{Name: "c-node1", Held: true, Busy: 1},
		{Name: "c-node2", Held: true},
	}}
	return o
}

func TestAReleaseDecisionReleasesThatOneNodeAfterTheHold(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &releasingNodes{}
	d, err := New(func(string) (scale.Nodes, error) { return n, nil }, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	o := twoIdleNodesForOne()
	if dec, err := d.Pass(context.Background(), "liveweb", o, 2); err != nil || dec.Action != scale.None {
		t.Fatalf("first sight decided %v (%v); the hold has not run", dec.Action, err)
	}
	now = now.Add(scale.DefaultHold)
	dec, err := d.Pass(context.Background(), "liveweb", o, 2)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if dec.Action != scale.Release || len(n.released) != 1 || n.released[0] != "c-node2" {
		t.Fatalf("past the hold decided %v and released %v, want Release of c-node2 alone: %s",
			dec.Action, n.released, dec.Reason)
	}
	if n.teardowns != 0 {
		t.Fatalf("a single-node release called the whole-cluster Teardown %d times", n.teardowns)
	}
	if got := d.State("liveweb").Nodes; got != 1 {
		t.Fatalf("state.Nodes = %d after the release, want 1", got)
	}
}

func TestAFailedReleaseLeavesTheStateWhereItWas(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &releasingNodes{fakeNodes: fakeNodes{failWith: errors.New("a pod landed on it after the cordon")}}
	d, err := New(func(string) (scale.Nodes, error) { return n, nil }, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	o := twoIdleNodesForOne()
	if _, err := d.Pass(context.Background(), "liveweb", o, 2); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	before := d.State("liveweb")
	now = now.Add(scale.DefaultHold)
	if _, err := d.Pass(context.Background(), "liveweb", o, 2); err == nil {
		t.Fatal("a failed release was reported as a completed pass")
	}
	if after := d.State("liveweb"); after.Nodes != before.Nodes || !after.NodeIdleSince["c-node2"].Equal(before.NodeIdleSince["c-node2"]) {
		t.Fatalf("a failed release advanced the state from %+v to %+v", before, after)
	}
}

func TestAnActuatorThatCannotReleaseOneNodeIsAnError(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &fakeNodes{}
	d := driver(t, &now, n)
	o := twoIdleNodesForOne()
	if _, err := d.Pass(context.Background(), "liveweb", o, 2); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	now = now.Add(scale.DefaultHold)
	if _, err := d.Pass(context.Background(), "liveweb", o, 2); !errors.Is(err, ErrNoReleaser) {
		t.Fatalf("err = %v, want ErrNoReleaser", err)
	}
	if n.teardowns != 0 {
		t.Fatal("an actuator that cannot release one node had the whole cluster torn down instead")
	}
}
