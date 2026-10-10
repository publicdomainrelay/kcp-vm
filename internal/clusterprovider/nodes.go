package clusterprovider

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

type NodeObserver struct {
	Client func(ctx context.Context, cluster string) (kubernetes.Interface, error)

	Baseline func(cluster string) (scale.Baseline, error)
}

func (o NodeObserver) Observe(ctx context.Context, cluster string) (scale.Observation, int, error) {
	if o.Client == nil {
		return scale.Observation{}, 0, fmt.Errorf(
			"clusterprovider: NodeObserver has no client, so %s cannot be read", cluster)
	}
	cs, err := o.Client(ctx, cluster)
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: the client for %s: %w", cluster, err)
	}

	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: listing pods in %s: %w", cluster, err)
	}
	jobs, err := cs.BatchV1().Jobs(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: listing jobs in %s: %w", cluster, err)
	}
	deployments, err := cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: listing deployments in %s: %w", cluster, err)
	}
	statefulsets, err := cs.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: listing statefulsets in %s: %w", cluster, err)
	}
	daemonsets, err := cs.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: listing daemonsets in %s: %w", cluster, err)
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: listing nodes in %s: %w", cluster, err)
	}
	replicasets, err := cs.AppsV1().ReplicaSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scale.Observation{}, 0, fmt.Errorf("clusterprovider: listing replicasets in %s: %w", cluster, err)
	}

	appliedByTheControlPlane := map[string]bool{}
	for i := range replicasets.Items {
		rs := &replicasets.Items[i]
		if scale.AppliedByTheControlPlane(rs.ObjectMeta) {
			appliedByTheControlPlane[rs.Namespace+"/"+rs.Name] = true
		}
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		for _, ref := range p.OwnerReferences {
			if ref.Kind != "ReplicaSet" {
				continue
			}
			if !appliedByTheControlPlane[p.Namespace+"/"+ref.Name] {
				continue
			}
			scale.StampAppliedByTheControlPlane(&p.ObjectMeta)
			break
		}
	}

	var baseline scale.Baseline
	if o.Baseline != nil {
		baseline, err = o.Baseline(cluster)
		if err != nil {
			return scale.Observation{}, 0, fmt.Errorf("clusterprovider: the add-on baseline of %s: %w", cluster, err)
		}
	}

	return scale.Narrow(scale.Observation{
		Pods:         pods.Items,
		Jobs:         jobs.Items,
		Deployments:  deployments.Items,
		StatefulSets: statefulsets.Items,
		DaemonSets:   daemonsets.Items,
	}, replicasets.Items, baseline), len(nodes.Items), nil
}
