package scale

import (
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func node(name string, created time.Duration) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(at(created))}}
}

func podOn(ns, name, nodeName string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func held(p Placement) Placement {
	for i := range p.Nodes {
		p.Nodes[i].Held = true
	}
	return p
}

func oneReplica() appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1)},
	}
}

type release struct {
	node string
	at   time.Duration
}

func driveScaleIn(t *testing.T, o Observation, st State, until time.Duration, opts Options) ([]release, State) {
	t.Helper()
	var released []release
	for now := time.Duration(0); now <= until; now += 10 * time.Second {
		d := Decide(o, st, at(now), opts)
		if d.Action == Provision || d.Action == Teardown {
			t.Fatalf("at %s the decision was %s/%d (%s); only releases are expected here", now,
				d.Action, d.Nodes, d.Reason)
		}
		if d.Action == Release {
			if d.Nodes != st.Nodes-1 || d.Release == "" {
				t.Fatalf("a release decided %d nodes naming %q from %d", d.Nodes, d.Release, st.Nodes)
			}
			released = append(released, release{node: d.Release, at: now})
			o.Placement.Nodes = slices.DeleteFunc(o.Placement.Nodes, func(n NodeLoad) bool {
				return n.Name == d.Release
			})
		}
		st = st.Next(d, at(now))
		st.Nodes = len(o.Placement.Nodes)
	}
	return released, st
}

func TestThreeIdleNodesWithDemandForOneReleaseTwoOverTwoHoldsAndKeepOne(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{oneReplica()}}
	o.Placement = held(Loads([]corev1.Node{node("c-node1", 0), node("c-node2", time.Second), node("c-node3", 2*time.Second)}, o, nil, Baseline{}))
	if Target(o, Options{}) != 1 {
		t.Fatalf("the fixture's target is %d, want 1", Target(o, Options{}))
	}
	released, st := driveScaleIn(t, o, State{Nodes: 3}, 6*DefaultHold, Options{})
	want := []release{{"c-node3", DefaultHold}, {"c-node2", 2 * DefaultHold}}
	if !slices.Equal(released, want) {
		t.Fatalf("three idle nodes for a target of one released %v, want %v: one node per hold, "+
			"newest first, the hold restarting for the rest after each release, and never below "+
			"the target", released, want)
	}
	if st.Nodes != 1 {
		t.Fatalf("the cluster ended at %d nodes, want the target of 1", st.Nodes)
	}
}

func TestANodeWithARunningNonBaselinePodIsNeverReleased(t *testing.T) {
	addon := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "external-secrets", Name: "external-secrets"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1)},
	}
	hash, _ := SpecHash(addon.Spec)
	baseline := Baseline{Workloads: []BaselineEntry{{WorkloadKey: WorkloadKey{Kind: KindDeployment,
		Namespace: addon.Namespace, Name: addon.Name}, SpecHash: hash}}}
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: addon.Namespace, Name: "external-secrets-5d",
		OwnerReferences: controlledBy(KindDeployment, addon.Name)}}
	addonPod := podOn(addon.Namespace, "external-secrets-5d-x", "c-node3", corev1.PodRunning)
	addonPod.OwnerReferences = controlledBy(KindReplicaSet, rs.Name)

	raw := Observation{
		Deployments: []appsv1.Deployment{addon},
		Pods: []corev1.Pod{
			podOn("default", "work", "c-node1", corev1.PodRunning),
			addonPod,
		},
	}
	nodes := []corev1.Node{node("c-node1", 0), node("c-node2", time.Second), node("c-node3", 2*time.Second)}
	raw.Placement = held(Loads(nodes, raw, []appsv1.ReplicaSet{rs}, baseline))
	o := Narrow(raw, []appsv1.ReplicaSet{rs}, baseline)
	if Target(o, Options{}) != 1 {
		t.Fatalf("the fixture's target is %d, want 1 (the bare pod; the add-on is baseline)", Target(o, Options{}))
	}
	released, st := driveScaleIn(t, o, State{Nodes: 3}, 10*DefaultHold, Options{})
	want := []release{{"c-node2", DefaultHold}, {"c-node3", 2 * DefaultHold}}
	if !slices.Equal(released, want) {
		t.Fatalf("released %v, want %v: c-node1 runs a tenant pod and is never released, and c-node3 "+
			"holds only a baseline pod, so it goes, but after c-node2, which holds nothing at all "+
			"(emptiest before newest)", released, want)
	}
	if st.Nodes != 1 {
		t.Fatalf("ended at %d nodes, want 1", st.Nodes)
	}
}

func TestANodeThatBecameIdleLessThanTheHoldAgoIsKept(t *testing.T) {
	nodes := []corev1.Node{node("c-node1", 0), node("c-node2", time.Second)}
	idle := Observation{Pods: []corev1.Pod{podOn("default", "web", "c-node1", corev1.PodRunning)}}
	idle.Placement = held(Loads(nodes, idle, nil, Baseline{}))
	busy := idle
	busy.Pods = append(slices.Clone(idle.Pods), podOn("default", "job", "c-node2", corev1.PodRunning))
	busy.Placement = held(Loads(nodes, busy, nil, Baseline{}))
	if Target(busy, Options{PodsPerNode: 2}) != 1 {
		t.Fatalf("the fixture's target is %d, want 1", Target(busy, Options{PodsPerNode: 2}))
	}
	opts := Options{PodsPerNode: 2}

	st := State{Nodes: 2}
	steps := []struct {
		now time.Duration
		o   Observation
	}{
		{0, busy}, {time.Minute, busy}, {time.Minute + time.Second, idle},
		{DefaultHold, idle}, {time.Minute + DefaultHold, idle},
	}
	for _, step := range steps {
		d := Decide(step.o, st, at(step.now), opts)
		if d.Action != None {
			t.Fatalf("at %s decided %s %q (%s): c-node1 runs a tenant pod and c-node2 has been idle "+
				"only since 1m1s, so nothing has been idle for the %s hold", step.now, d.Action,
				d.Release, d.Reason, DefaultHold)
		}
		st = st.Next(d, at(step.now))
	}
	if since := st.NodeIdleSince["c-node2"]; !since.Equal(at(time.Minute + time.Second)) {
		t.Fatalf("c-node2's idle clock reads %s, want 1m1s: a node running a tenant pod has no idle clock, "+
			"and the clock starts at the first pass that sees it idle", since.Sub(t0))
	}
	d := Decide(idle, st, at(time.Minute+time.Second+DefaultHold), opts)
	if d.Action != Release || d.Release != "c-node2" {
		t.Fatalf("one hold after c-node2 went idle the decision was %s %q, want Release c-node2: %s",
			d.Action, d.Release, d.Reason)
	}
}

func TestTheHoldIsExactForOneIdleNode(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{oneReplica()}}
	o.Placement = held(Loads([]corev1.Node{node("c-node1", 0), node("c-node2", time.Second)}, o, nil, Baseline{}))
	st := State{Nodes: 2, NodeIdleSince: map[string]time.Time{"c-node1": at(0), "c-node2": at(0)}}
	if d := Decide(o, st, at(DefaultHold-time.Second), Options{}); d.Action != None {
		t.Fatalf("one second inside the hold decided %s", d.Action)
	}
	d := Decide(o, st, at(DefaultHold), Options{})
	if d.Action != Release || d.Release != "c-node2" || d.Nodes != 1 {
		t.Fatalf("at the hold decided %s/%d %q, want Release/1 c-node2: %s", d.Action, d.Nodes, d.Release, d.Reason)
	}
}

func TestScaleInNeverGoesBelowTheTarget(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(2)},
	}}}
	o.Placement = held(Loads([]corev1.Node{node("a", 0), node("b", time.Second), node("c", 2*time.Second)}, o, nil, Baseline{}))
	released, st := driveScaleIn(t, o, State{Nodes: 3}, 10*DefaultHold, Options{PodsPerNode: 1})
	if len(released) != 1 || st.Nodes != 2 {
		t.Fatalf("a target of 2 over three idle nodes released %v and ended at %d", released, st.Nodes)
	}
}

func TestAPodAboutToBeScheduledHoldsItsNode(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{oneReplica()}}
	nominated := podOn("default", "web-1", "", corev1.PodPending)
	nominated.Status.NominatedNodeName = "c-node2"
	pinned := podOn("default", "web-2", "", corev1.PodPending)
	pinned.Spec.NodeSelector = map[string]string{HostnameLabel: "c-node3"}
	o.Pods = []corev1.Pod{nominated, pinned}
	o.Placement = held(Loads([]corev1.Node{node("c-node1", 0), node("c-node2", time.Second), node("c-node3", 2*time.Second)}, o, nil, Baseline{}))
	for _, name := range []string{"c-node2", "c-node3"} {
		if l, _ := o.Placement.Load(name); !l.Incoming {
			t.Fatalf("%s is not marked as a pod's destination", name)
		}
	}
	st := State{Nodes: 3, NodeIdleSince: map[string]time.Time{"c-node1": at(0), "c-node2": at(0), "c-node3": at(0)}}
	d := Decide(o, st, at(10*DefaultHold), Options{})
	if d.Action != Release || d.Release != "c-node1" {
		t.Fatalf("decided %s %q, want Release c-node1: the other two are a pending pod's "+
			"destination", d.Action, d.Release)
	}
}

func TestAnUnplacedPendingPodHoldsEveryReleaseUntilItIsOlderThanTheHold(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{oneReplica()}}
	fresh := podOn("default", "web-x", "", corev1.PodPending)
	fresh.CreationTimestamp = metav1.NewTime(at(9 * DefaultHold))
	o.Pods = []corev1.Pod{fresh}
	o.Placement = held(Loads([]corev1.Node{node("c-node1", 0), node("c-node2", time.Second)}, o, nil, Baseline{}))
	st := State{Nodes: 2, NodeIdleSince: map[string]time.Time{"c-node1": at(0), "c-node2": at(0)}}
	if d := Decide(o, st, at(10*DefaultHold-time.Second), Options{}); d.Action != None {
		t.Fatalf("with a pod Pending for less than the hold and no node named, decided %s %q", d.Action, d.Release)
	}
	if d := Decide(o, st, at(10*DefaultHold), Options{}); d.Action != Release {
		t.Fatalf("with the only Pending pod waiting the whole hold, decided %s: %s", d.Action, d.Reason)
	}
	unknownAge := o
	unknownAge.Pods = []corev1.Pod{podOn("default", "web-y", "", corev1.PodPending)}
	unknownAge.Placement = held(Loads([]corev1.Node{node("c-node1", 0), node("c-node2", time.Second)}, unknownAge, nil, Baseline{}))
	if d := Decide(unknownAge, st, at(10*DefaultHold), Options{}); d.Action != None {
		t.Fatalf("a Pending pod with no creation time did not hold the releases: %s", d.Action)
	}
}

func TestANodeThisProviderDoesNotHoldIsNeverReleased(t *testing.T) {
	o := Observation{Deployments: []appsv1.Deployment{oneReplica()}}
	o.Placement = Loads([]corev1.Node{node("c-node1", 0), node("c-node2", time.Second)}, o, nil, Baseline{})
	st := State{Nodes: 2, NodeIdleSince: map[string]time.Time{"c-node1": at(0), "c-node2": at(0)}}
	if d := Decide(o, st, at(10*DefaultHold), Options{}); d.Action != None {
		t.Fatalf("a node no provider process holds was decided %s %q; nothing here can release it", d.Action, d.Release)
	}
}

func TestLoadsCountsOnlyWhatHoldsANode(t *testing.T) {
	ds := podOn("obs", "agent-x", "n", corev1.PodRunning)
	ds.OwnerReferences = controlledBy(KindDaemonSet, "agent")
	mirror := podOn("kube-system", "static-n", "n", corev1.PodRunning)
	mirror.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "x"}
	k3s := podOn("kube-system", "coredns-x", "n", corev1.PodRunning)
	StampAppliedByTheControlPlane(&k3s.ObjectMeta)
	done := podOn("ci", "build-0", "n", corev1.PodSucceeded)
	work := podOn("ci", "build-1", "n", corev1.PodRunning)
	starting := podOn("ci", "build-2", "n", corev1.PodPending)
	elsewhere := podOn("ci", "build-3", "gone", corev1.PodRunning)
	o := Observation{Pods: []corev1.Pod{ds, mirror, k3s, done, work, starting, elsewhere}}
	p := Loads([]corev1.Node{node("n", 0)}, o, nil, Baseline{})
	l, _ := p.Load("n")
	if l.Busy != 2 || l.Evictable != 1 || l.Incoming || len(p.Unplaced) != 0 {
		t.Fatalf("load = %+v unplaced %v, want Busy 2 (the Running and the bound Pending tenant pods), "+
			"Evictable 1 (the k3s add-on pod), no DaemonSet, mirror or terminal pod counted", l, p.Unplaced)
	}
}

func TestAReleaseRestartsTheHoldOfTheNodesLeft(t *testing.T) {
	clocks := map[string]time.Time{"a": at(0), "b": at(0), "c": at(time.Minute)}
	next := State{Nodes: 3, NodeIdleSince: clocks}.Next(Decision{Action: Release, Nodes: 2, Release: "a", NodeIdleSince: clocks}, at(DefaultHold))
	want := map[string]time.Time{"b": at(DefaultHold), "c": at(DefaultHold)}
	if len(next.NodeIdleSince) != 2 || !next.NodeIdleSince["b"].Equal(want["b"]) || !next.NodeIdleSince["c"].Equal(want["c"]) || next.Nodes != 2 {
		t.Fatalf("after releasing a the state is %+v, want b and c restarted at the release", next)
	}
	if clocks["b"] != at(0) {
		t.Fatal("Next wrote into the decision's map; the state must be a value")
	}
}
