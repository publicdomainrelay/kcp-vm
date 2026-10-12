package providermain

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

type observeCluster func(ctx context.Context, cluster string) (scale.Observation, int, error)

var _ scale.Releaser = (*tenantNodes)(nil)

var errNoReleaseObserver = errors.New(
	"clusterprovider: releasing one node re-reads the cluster after the cordon, and no observer was " +
		"supplied -- a pod the scheduler bound between the decision's read and the cordon would be " +
		"evicted and its node released under it")

var errReleaseNodeBusy = errors.New(
	"clusterprovider: the node took work between the decision's read and the cordon, so it is " +
		"uncordoned and kept; the next pass decides again")

func (h *nodeHosts) observeHeld(observe observeCluster) observeCluster {
	return func(ctx context.Context, cluster string) (scale.Observation, int, error) {
		o, nodes, err := observe(ctx, cluster)
		if err != nil {
			return o, nodes, err
		}
		held := h.heldNames(cluster)
		for i := range o.Placement.Nodes {
			o.Placement.Nodes[i].Held = held[o.Placement.Nodes[i].Name]
		}
		return o, nodes + h.inFlight(ctx, cluster), nil
	}
}

func (h *nodeHosts) heldNames(cluster string) map[string]bool {
	h.mu.Lock()
	n, ok := h.hosts[cluster]
	h.mu.Unlock()
	if !ok {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]bool{}
	for _, node := range n.heldNodesLocked() {
		out[node.name] = true
	}
	return out
}

func (n *tenantNodes) Release(ctx context.Context, name string) error {
	n.mu.Lock()
	var node heldNode
	found := false
	for _, h := range n.heldNodesLocked() {
		if h.name == name {
			node, found = h, true
			break
		}
	}
	n.mu.Unlock()
	if !found {
		return fmt.Errorf("clusterprovider: node %s of %s is not held by this provider process, so "+
			"there is no claim here to delete and no guest to release", name, n.cluster)
	}
	if n.opts.Observe == nil {
		return errNoReleaseObserver
	}
	client, err := n.client(ctx)
	if err != nil {
		return err
	}
	cordoned, err := setUnschedulable(ctx, client, name, true)
	if err != nil {
		return fmt.Errorf("clusterprovider: cordoning node %s of %s before releasing it: %w", name, n.cluster, err)
	}
	uncordon := func(cause error) error {
		if !cordoned {
			return cause
		}
		if _, err := setUnschedulable(ctx, client, name, false); err != nil {
			return errors.Join(cause, fmt.Errorf("clusterprovider: uncordoning node %s of %s after "+
				"its release stopped: %w", name, n.cluster, err))
		}
		return cause
	}
	o, _, err := n.opts.Observe(ctx, n.cluster)
	if err != nil {
		return uncordon(fmt.Errorf("clusterprovider: re-reading %s after cordoning %s: %w", n.cluster, name, err))
	}
	load, seen := o.Placement.Load(name)
	if !seen || load.Busy > 0 || load.Incoming {
		n.log.Info("cluster provider: a node chosen for release took work before the cordon, so it is kept",
			"cluster", n.cluster, "node", name, "busy", load.Busy, "incoming", load.Incoming, "seen", seen)
		return uncordon(fmt.Errorf("%w: node %s of %s", errReleaseNodeBusy, name, n.cluster))
	}
	evicted, err := drainNode(ctx, client, name)
	if err != nil {
		return uncordon(fmt.Errorf("clusterprovider: draining node %s of %s: %w -- the node is "+
			"uncordoned and kept, and the next pass decides again", name, n.cluster, err))
	}
	n.log.Info("cluster provider: a node held nothing beyond the baseline for the hold, so it is "+
		"cordoned, drained and released",
		"cluster", n.cluster, "node", name, "evicted", evicted)
	if err := n.teardownHeld(ctx, node); err != nil {
		return err
	}
	return n.writeSnapshot()
}

func setUnschedulable(ctx context.Context, client kubernetes.Interface, name string, unschedulable bool) (bool, error) {
	node, err := client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if node.Spec.Unschedulable == unschedulable {
		return false, nil
	}
	node.Spec.Unschedulable = unschedulable
	if _, err := client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return false, err
	}
	return true, nil
}

func drainNode(ctx context.Context, client kubernetes.Interface, name string) ([]string, error) {
	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + name,
	})
	if err != nil {
		return nil, fmt.Errorf("listing the pods on %s: %w", name, err)
	}
	var evicted []string
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName != name || p.DeletionTimestamp != nil || scale.FollowsTheNodes(p) {
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		err := client.CoreV1().Pods(p.Namespace).EvictV1(ctx, &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: p.Name},
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return evicted, fmt.Errorf("evicting %s/%s: %w", p.Namespace, p.Name, err)
		}
		evicted = append(evicted, p.Namespace+"/"+p.Name)
	}
	return evicted, nil
}
