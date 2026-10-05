package cloudprovider

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

const LabelCluster = "node.socialweb.computer/cluster"

const LabelInstanceType = "node.socialweb.computer/instance-type"

type Patch struct {
	ProviderID string

	SetLabels map[string]string

	RemoveLabels []string

	Delete bool

	Reason string
}

func Desired(node corev1.Node, inst Instance, known bool) (Patch, bool) {
	if !known {
		return Patch{
			Delete: true,
			Reason: fmt.Sprintf(
				"there is no instance record for %s, so the guest behind this Node is gone and "+
					"the Node is deleted", node.Name),
		}, true
	}
	if inst.Draining {
		return Patch{
			Delete: true,
			Reason: fmt.Sprintf(
				"the instance for %s is Draining, so the Node is deleted rather than left to "+
					"advertise a guest that is on its way out", node.Name),
		}, true
	}
	if inst.Cluster == "" || inst.NodeName == "" {
		return Patch{
			Reason: fmt.Sprintf(
				"the instance record for %s names no cluster or no node (cluster=%q, node=%q), so "+
					"it yields no provider ID. A malformed record is not an absent one: nothing "+
					"is written, because stamping nothing while the Node keeps an identity from a "+
					"previous life is the state this controller exists to end",
				node.Name, inst.Cluster, inst.NodeName),
		}, false
	}

	providerID := ProviderID(inst.Cluster, inst.NodeName)

	set := map[string]string{LabelCluster: inst.Cluster}
	remove := []string{}
	if inst.InstanceType != "" {
		set[LabelInstanceType] = inst.InstanceType
	} else {
		remove = append(remove, LabelInstanceType)
	}

	unchanged := node.Spec.ProviderID == providerID
	for k, v := range set {
		if node.Labels[k] != v {
			unchanged = false
		}
	}
	for _, k := range remove {
		if _, present := node.Labels[k]; present {
			unchanged = false
		}
	}
	if unchanged {
		return Patch{
			ProviderID:   providerID,
			SetLabels:    set,
			RemoveLabels: remove,
			Reason: fmt.Sprintf(
				"the Node %s already carries the provider ID and labels its instance gives it, "+
					"so nothing is written", node.Name),
		}, false
	}
	return Patch{
		ProviderID:   providerID,
		SetLabels:    set,
		RemoveLabels: remove,
		Reason: fmt.Sprintf(
			"the Node %s is stamped with the provider ID and labels of the instance that "+
				"backs it", node.Name),
	}, true
}
