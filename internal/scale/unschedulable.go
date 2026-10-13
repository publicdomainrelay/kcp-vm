package scale

import (
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const UnschedulableSettle = 10 * time.Second

type Unschedulable struct {
	Pod string

	SeenNodes int

	Message string
}

type Spread struct {
	Group string

	Pods int
}

type Want struct {
	Target int

	Wanted int

	Why string

	Waiting []string
}

func NodeReady(n *corev1.Node) (bool, time.Time) {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue, c.LastTransitionTime.Time
		}
	}
	return false, time.Time{}
}

func unschedulable(p *corev1.Pod) (Unschedulable, bool) {
	for _, c := range p.Status.Conditions {
		if c.Type != corev1.PodScheduled || c.Status != corev1.ConditionFalse || c.Reason != corev1.PodReasonUnschedulable {
			continue
		}
		seen := -1
		var n int
		if _, err := fmt.Sscanf(c.Message, "0/%d nodes are available", &n); err == nil {
			seen = n
		}
		return Unschedulable{Pod: p.Namespace + "/" + p.Name, SeenNodes: seen, Message: c.Message}, true
	}
	return Unschedulable{}, false
}

func hostnameAntiAffinity(p *corev1.Pod) []string {
	a := p.Spec.Affinity
	if a == nil || a.PodAntiAffinity == nil {
		return nil
	}
	var out []string
	for _, term := range a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != HostnameLabel || term.NamespaceSelector != nil || term.LabelSelector == nil {
			continue
		}
		if len(term.Namespaces) > 0 && !slices.Equal(term.Namespaces, []string{p.Namespace}) {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
		if err != nil || sel.Empty() || !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		out = append(out, p.Namespace+"/"+metav1.FormatLabelSelector(term.LabelSelector))
	}
	return out
}

func spreadOf(pods []*corev1.Pod) Spread {
	groups := map[string]int{}
	for _, p := range pods {
		for _, g := range hostnameAntiAffinity(p) {
			groups[g]++
		}
	}
	var out Spread
	for g, n := range groups {
		if n > out.Pods || (n == out.Pods && g < out.Group) {
			out = Spread{Group: g, Pods: n}
		}
	}
	return out
}

func (p Placement) settled(now time.Time) (bool, string) {
	if len(p.Nodes) == 0 {
		return false, "no node yet"
	}
	for _, n := range p.Nodes {
		if !n.Ready {
			return false, fmt.Sprintf("node %s is not Ready", n.Name)
		}
		if age := now.Sub(n.ReadySince); age < UnschedulableSettle {
			return false, fmt.Sprintf("node %s has been Ready for %s, under the %s the scheduler "+
				"gets to try the Pending pods on it", n.Name, age.Round(time.Second), UnschedulableSettle)
		}
	}
	return true, ""
}

func (p Placement) blocked(now time.Time, inFlight int) ([]string, string) {
	if len(p.Unschedulable) == 0 {
		return nil, ""
	}
	if inFlight > 0 {
		return nil, fmt.Sprintf("%d node(s) are still joining", inFlight)
	}
	if ok, why := p.settled(now); !ok {
		return nil, why
	}
	var pods []string
	for _, u := range p.Unschedulable {
		if u.SeenNodes == len(p.Nodes) {
			pods = append(pods, u.Pod)
		}
	}
	if len(pods) == 0 {
		return nil, fmt.Sprintf("the scheduler has not yet tried the %d unschedulable pod(s) on all %d nodes",
			len(p.Unschedulable), len(p.Nodes))
	}
	for _, n := range p.Nodes {
		if n.idle() {
			return nil, fmt.Sprintf("pod(s) %s are unschedulable although node %s holds none of the "+
				"workload, so a fresh market node would refuse them too: no node is added for them",
				strings.Join(pods, ", "), n.Name)
		}
	}
	return pods, ""
}

func Wanted(o Observation, opts Options, now time.Time) Want {
	if !Intent(o) {
		return Want{}
	}
	per := opts.podsPerNode()
	w := Want{Wanted: (Demand(o) + per - 1) / per}
	w.Why = fmt.Sprintf("%d pod(s) of demand at %d per node", Demand(o), per)
	if w.Wanted < 1 {
		w.Wanted = 1
	}
	if s := o.Placement.Spread; s.Pods > w.Wanted {
		w.Wanted = s.Pods
		w.Why = fmt.Sprintf("%d pods of %s repel each other by required pod anti-affinity on %s",
			s.Pods, s.Group, HostnameLabel)
	}
	pods, wait := o.Placement.blocked(now, o.InFlight)
	if len(pods) > 0 && len(o.Placement.Nodes)+1 > w.Wanted {
		w.Wanted = len(o.Placement.Nodes) + 1
		w.Why = fmt.Sprintf("the scheduler refused pod(s) %s on all %d Ready nodes",
			strings.Join(pods, ", "), len(o.Placement.Nodes))
	}
	if len(pods) == 0 && wait != "" {
		w.Why += "; unschedulable pods add no node yet: " + wait
	}
	w.Waiting = pods
	w.Target = min(w.Wanted, opts.maxNodes())
	return w
}
