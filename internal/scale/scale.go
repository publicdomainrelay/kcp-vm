package scale

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const DefaultHold = 2 * time.Minute

const DefaultMaxNodes = 10

const DefaultPodsPerNode = 8

const AddonOwnerAnnotation = "objectset.rio.cattle.io/owner-gvk"

const K3sAddonOwnerGVK = "k3s.cattle.io/v1, Kind=Addon"

const AddonOwnerNameAnnotation = "objectset.rio.cattle.io/owner-name"

func AppliedByTheControlPlane(meta metav1.ObjectMeta) bool {
	return meta.Annotations[AddonOwnerAnnotation] == K3sAddonOwnerGVK
}

func StampAppliedByTheControlPlane(meta *metav1.ObjectMeta) {
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[AddonOwnerAnnotation] = K3sAddonOwnerGVK
	meta.Annotations[AddonOwnerNameAnnotation] = "inherited from the owner this object was created by"
}

type Observation struct {
	Pods         []corev1.Pod
	Jobs         []batchv1.Job
	Deployments  []appsv1.Deployment
	StatefulSets []appsv1.StatefulSet
	DaemonSets   []appsv1.DaemonSet

	Placement Placement

	UnreconciledCustomResources []CustomResource
}

func Intent(o Observation) bool {
	if len(o.UnreconciledCustomResources) > 0 {
		return true
	}
	for i := range o.Pods {
		p := &o.Pods[i]
		if AppliedByTheControlPlane(p.ObjectMeta) || FollowsTheNodes(p) {
			continue
		}
		switch p.Status.Phase {
		case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
			return true
		}
	}
	for i := range o.Jobs {
		j := &o.Jobs[i]
		if AppliedByTheControlPlane(j.ObjectMeta) {
			continue
		}
		if j.Status.CompletionTime != nil {
			continue
		}
		if j.Spec.Suspend != nil && *j.Spec.Suspend {
			continue
		}
		return true
	}
	for i := range o.Deployments {
		d := &o.Deployments[i]
		if AppliedByTheControlPlane(d.ObjectMeta) {
			continue
		}
		if replicas(d.Spec.Replicas) > 0 {
			return true
		}
	}
	for i := range o.StatefulSets {
		s := &o.StatefulSets[i]
		if AppliedByTheControlPlane(s.ObjectMeta) {
			continue
		}
		if replicas(s.Spec.Replicas) > 0 {
			return true
		}
	}
	return false
}

func replicas(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

func Demand(o Observation) int {
	total := len(o.UnreconciledCustomResources)
	for i := range o.Pods {
		p := &o.Pods[i]
		if AppliedByTheControlPlane(p.ObjectMeta) || FollowsTheNodes(p) {
			continue
		}
		switch p.Status.Phase {
		case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
			total++
		}
	}
	for i := range o.Jobs {
		j := &o.Jobs[i]
		if AppliedByTheControlPlane(j.ObjectMeta) {
			continue
		}
		if j.Status.CompletionTime != nil {
			continue
		}
		if j.Spec.Suspend != nil && *j.Spec.Suspend {
			continue
		}
		total += jobStillNeeds(j)
	}
	for i := range o.Deployments {
		d := &o.Deployments[i]
		if AppliedByTheControlPlane(d.ObjectMeta) {
			continue
		}
		if r := int(replicas(d.Spec.Replicas)); r > 0 {
			total += r
		}
	}
	for i := range o.StatefulSets {
		s := &o.StatefulSets[i]
		if AppliedByTheControlPlane(s.ObjectMeta) {
			continue
		}
		if r := int(replicas(s.Spec.Replicas)); r > 0 {
			total += r
		}
	}
	return total
}

func jobStillNeeds(j *batchv1.Job) int {
	want := 1
	if j.Spec.Completions != nil {
		want = int(*j.Spec.Completions)
	}
	needs := want - int(j.Status.Succeeded)
	if needs < 1 {
		needs = 1
	}
	return needs
}

type Action int

const (
	None Action = iota

	Provision

	Teardown

	Release
)

func (a Action) String() string {
	switch a {
	case Provision:
		return "Provision"
	case Teardown:
		return "Teardown"
	case Release:
		return "Release"
	case None:
		return "None"
	}
	return fmt.Sprintf("Action(%d)", int(a))
}

type State struct {
	Nodes int

	IdleSince time.Time

	Backoff Backoff

	NodeIdleSince map[string]time.Time
}

type Decision struct {
	Action Action

	Nodes int

	Intent bool

	Idle time.Duration

	BackoffUntil time.Time

	Release string

	NodeIdleSince map[string]time.Time

	Reason string
}

type Options struct {
	Hold time.Duration

	MaxNodes int

	PodsPerNode int

	BackoffBase time.Duration

	BackoffCap time.Duration
}

func (o Options) hold() time.Duration {
	switch {
	case o.Hold == 0:
		return DefaultHold
	case o.Hold < 0:
		return 0
	}
	return o.Hold
}

func (o Options) maxNodes() int {
	if o.MaxNodes <= 0 {
		return DefaultMaxNodes
	}
	return o.MaxNodes
}

func (o Options) podsPerNode() int {
	if o.PodsPerNode <= 0 {
		return DefaultPodsPerNode
	}
	return o.PodsPerNode
}

func Target(o Observation, opts Options) int {
	if !Intent(o) {
		return 0
	}
	per := opts.podsPerNode()
	ceiling := opts.maxNodes()
	target := (Demand(o) + per - 1) / per
	if target < 1 {
		target = 1
	}
	if target > ceiling {
		target = ceiling
	}
	if target < 1 {
		target = 1
	}
	return target
}

func Decide(o Observation, st State, now time.Time, opts Options) Decision {
	d := decide(o, st, now, opts)
	if d.NodeIdleSince == nil {
		d.NodeIdleSince = nodeClocks(o.Placement, st.NodeIdleSince, now)
	}
	return d
}

func decide(o Observation, st State, now time.Time, opts Options) Decision {
	intent := Intent(o)

	if intent {
		target := Target(o, opts)
		switch {
		case st.Nodes < target && st.Backoff.Holds(now):
			return backingOff(st, target, now)
		case st.Nodes < target:
			if st.Nodes == 0 {
				return Decision{
					Action: Provision, Nodes: target, Intent: true,
					Reason: "intent and no node",
				}
			}
			return Decision{
				Action: Provision, Nodes: target, Intent: true,
				Reason: fmt.Sprintf("intent, and %d nodes are counted for a target of %d", st.Nodes, target),
			}
		case st.Nodes > target:
			return scaleIn(o, st, now, opts, target, nodeClocks(o.Placement, st.NodeIdleSince, now))
		default:
			return Decision{
				Action: None, Nodes: st.Nodes, Intent: true,
				Reason: "intent, and a node is already counted for this cluster; a second " +
					"provision here is a second node (ADR 0014: idempotency is per-cluster)",
			}
		}
	}

	if st.Nodes == 0 {
		return Decision{
			Action: None, Nodes: 0,
			Reason: "nothing running and no node",
		}
	}

	idleSince := st.IdleSince
	if idleSince.IsZero() {
		idleSince = now
	}
	idle := now.Sub(idleSince)

	if idle < opts.hold() {
		return Decision{
			Action: None, Nodes: st.Nodes, Idle: idle,
			Reason: fmt.Sprintf("nothing running, holding the node for %s more", opts.hold()-idle),
		}
	}
	return Decision{
		Action: Teardown, Nodes: 0, Idle: idle,
		Reason: fmt.Sprintf("nothing running for %s, past the %s hold", idle, opts.hold()),
	}
}

func (st State) Next(d Decision, now time.Time) State {
	switch d.Action {
	case Provision:
		return State{Nodes: d.Nodes, Backoff: st.Backoff, NodeIdleSince: d.NodeIdleSince}
	case Teardown:
		return State{Nodes: 0}
	case Release:
		return State{Nodes: d.Nodes, Backoff: st.Backoff,
			NodeIdleSince: restartedClocks(d.NodeIdleSince, d.Release, now)}
	}
	if d.Intent {
		return State{Nodes: st.Nodes, Backoff: st.Backoff, NodeIdleSince: d.NodeIdleSince}
	}
	if st.Nodes == 0 {
		return State{}
	}
	if !st.IdleSince.IsZero() {
		return State{Nodes: st.Nodes, IdleSince: st.IdleSince, NodeIdleSince: d.NodeIdleSince}
	}
	return State{Nodes: st.Nodes, IdleSince: now, NodeIdleSince: d.NodeIdleSince}
}

type Nodes interface {
	Provision(ctx context.Context) error

	Teardown(ctx context.Context) error
}

type Releaser interface {
	Release(ctx context.Context, node string) error
}
