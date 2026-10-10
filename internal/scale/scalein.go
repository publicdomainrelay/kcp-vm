package scale

import (
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const HostnameLabel = "kubernetes.io/hostname"

type NodeLoad struct {
	Name string

	Created time.Time

	Held bool

	Busy int

	Evictable int

	Incoming bool
}

type Placement struct {
	Nodes []NodeLoad

	Unplaced []time.Time
}

func (p Placement) Load(name string) (NodeLoad, bool) {
	for _, n := range p.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return NodeLoad{}, false
}

func (p Placement) placing(now time.Time, hold time.Duration) (time.Duration, bool) {
	for _, created := range p.Unplaced {
		if created.IsZero() {
			return 0, true
		}
		if age := now.Sub(created); age < hold {
			return age, true
		}
	}
	return 0, false
}

func ControlledByADaemonSet(p *corev1.Pod) bool {
	c := metav1.GetControllerOf(p)
	return c != nil && c.Kind == KindDaemonSet
}

func MirrorPod(p *corev1.Pod) bool {
	_, ok := p.Annotations[corev1.MirrorPodAnnotationKey]
	return ok
}

func FollowsTheNodes(p *corev1.Pod) bool {
	return ControlledByADaemonSet(p) || MirrorPod(p)
}

func live(p *corev1.Pod) bool {
	switch p.Status.Phase {
	case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
		return true
	}
	return false
}

func Loads(nodes []corev1.Node, o Observation, replicaSets []appsv1.ReplicaSet, b Baseline) Placement {
	ours := baselineWorkloads(o, b)
	deploymentOf := deploymentsOfReplicaSets(replicaSets)
	byName := map[string]*NodeLoad{}
	out := Placement{Nodes: make([]NodeLoad, 0, len(nodes))}
	for i := range nodes {
		out.Nodes = append(out.Nodes, NodeLoad{Name: nodes[i].Name, Created: nodes[i].CreationTimestamp.Time})
	}
	for i := range out.Nodes {
		byName[out.Nodes[i].Name] = &out.Nodes[i]
	}
	for i := range o.Pods {
		p := &o.Pods[i]
		if !live(p) || FollowsTheNodes(p) {
			continue
		}
		baseline := AppliedByTheControlPlane(p.ObjectMeta)
		if k, ok := workloadOf(p, deploymentOf); ok && ours[k] {
			baseline = true
		}
		if p.Spec.NodeName != "" {
			n, ok := byName[p.Spec.NodeName]
			if !ok {
				continue
			}
			if baseline {
				n.Evictable++
			} else {
				n.Busy++
			}
			continue
		}
		if baseline || p.Status.Phase != corev1.PodPending {
			continue
		}
		if target := destinedFor(p); target != "" {
			if n, ok := byName[target]; ok {
				n.Incoming = true
				continue
			}
		}
		out.Unplaced = append(out.Unplaced, p.CreationTimestamp.Time)
	}
	return out
}

func destinedFor(p *corev1.Pod) string {
	if p.Status.NominatedNodeName != "" {
		return p.Status.NominatedNodeName
	}
	if h := p.Spec.NodeSelector[HostnameLabel]; h != "" {
		return h
	}
	a := p.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return ""
	}
	for _, term := range a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, r := range term.MatchExpressions {
			if r.Key == HostnameLabel && r.Operator == corev1.NodeSelectorOpIn && len(r.Values) == 1 {
				return r.Values[0]
			}
		}
		for _, r := range term.MatchFields {
			if r.Key == "metadata.name" && r.Operator == corev1.NodeSelectorOpIn && len(r.Values) == 1 {
				return r.Values[0]
			}
		}
	}
	return ""
}

func baselineWorkloads(o Observation, b Baseline) map[WorkloadKey]bool {
	out := map[WorkloadKey]bool{}
	for _, d := range o.Deployments {
		k := WorkloadKey{Kind: KindDeployment, Namespace: d.Namespace, Name: d.Name}
		if AppliedByTheControlPlane(d.ObjectMeta) || b.Holds(k, d.Spec) {
			out[k] = true
		}
	}
	for _, s := range o.StatefulSets {
		k := WorkloadKey{Kind: KindStatefulSet, Namespace: s.Namespace, Name: s.Name}
		if AppliedByTheControlPlane(s.ObjectMeta) || b.Holds(k, s.Spec) {
			out[k] = true
		}
	}
	for _, j := range o.Jobs {
		k := WorkloadKey{Kind: KindJob, Namespace: j.Namespace, Name: j.Name}
		if AppliedByTheControlPlane(j.ObjectMeta) || b.Holds(k, j.Spec) {
			out[k] = true
		}
	}
	return out
}

func deploymentsOfReplicaSets(replicaSets []appsv1.ReplicaSet) map[WorkloadKey]WorkloadKey {
	out := map[WorkloadKey]WorkloadKey{}
	for i := range replicaSets {
		rs := &replicaSets[i]
		if c := metav1.GetControllerOf(rs); c != nil && c.Kind == KindDeployment {
			out[WorkloadKey{Kind: KindReplicaSet, Namespace: rs.Namespace, Name: rs.Name}] =
				WorkloadKey{Kind: KindDeployment, Namespace: rs.Namespace, Name: c.Name}
		}
	}
	return out
}

func (n NodeLoad) idle() bool {
	return n.Busy == 0 && !n.Incoming
}

func nodeClocks(p Placement, before map[string]time.Time, now time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(p.Nodes))
	for _, n := range p.Nodes {
		if !n.idle() {
			continue
		}
		if since, ok := before[n.Name]; ok {
			out[n.Name] = since
			continue
		}
		out[n.Name] = now
	}
	return out
}

func restartedClocks(clocks map[string]time.Time, released string, now time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(clocks))
	for name := range clocks {
		if name == released {
			continue
		}
		out[name] = now
	}
	return out
}

func releasable(p Placement, clocks map[string]time.Time, now time.Time, hold time.Duration) []NodeLoad {
	var out []NodeLoad
	for _, n := range p.Nodes {
		if !n.Held || !n.idle() {
			continue
		}
		since, ok := clocks[n.Name]
		if !ok || now.Sub(since) < hold {
			continue
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b NodeLoad) int {
		switch {
		case a.Evictable != b.Evictable:
			return a.Evictable - b.Evictable
		case !a.Created.Equal(b.Created):
			return b.Created.Compare(a.Created)
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

func scaleIn(o Observation, st State, now time.Time, opts Options, target int, clocks map[string]time.Time) Decision {
	hold := opts.hold()
	keep := Decision{Action: None, Nodes: st.Nodes, Intent: true, NodeIdleSince: clocks}
	if age, placing := o.Placement.placing(now, hold); placing {
		keep.Reason = fmt.Sprintf("intent, and %d nodes are counted for a target of %d, and a pod "+
			"created %s ago is Pending with no node yet: the scheduler may place it on any idle "+
			"node, so no node is released until it is placed or has waited out the %s hold",
			st.Nodes, target, age.Round(time.Second), hold)
		return keep
	}
	candidates := releasable(o.Placement, clocks, now, hold)
	if len(candidates) == 0 {
		keep.Reason = fmt.Sprintf("intent, and %d nodes are counted for a target of %d, and no "+
			"node this provider holds has held nothing beyond the baseline for the %s hold",
			st.Nodes, target, hold)
		return keep
	}
	pick := candidates[0]
	return Decision{
		Action: Release, Nodes: st.Nodes - 1, Intent: true, Release: pick.Name,
		Idle: now.Sub(clocks[pick.Name]), NodeIdleSince: clocks,
		Reason: fmt.Sprintf("intent, and %d nodes are counted for a target of %d; %s has held "+
			"nothing beyond the baseline for %s, past the %s hold, and is the emptiest and newest "+
			"of %d such nodes, so it alone is released this hold",
			st.Nodes, target, pick.Name, now.Sub(clocks[pick.Name]).Round(time.Second), hold,
			len(candidates)),
	}
}
