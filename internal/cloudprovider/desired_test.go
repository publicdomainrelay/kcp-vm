package cloudprovider

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nodeOf(name string) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestAnUnknownInstanceDeletesTheNode(t *testing.T) {
	patch, changed := Desired(nodeOf("c1-node1"), Instance{}, false)
	if !changed {
		t.Fatal("an unknown instance returned changed=false, so the caller would never delete")
	}
	if !patch.Delete {
		t.Fatal("an unknown instance did not ask for the Node to be deleted")
	}
	if patch.Reason == "" {
		t.Fatal("an unknown instance gave no reason; the reason is the only account of the delete")
	}
}

func TestADrainingInstanceDeletesTheNode(t *testing.T) {
	inst := Instance{NodeName: "c1-node1", Cluster: "c1", InstanceType: "small", Draining: true}
	patch, changed := Desired(nodeOf("c1-node1"), inst, true)
	if !changed || !patch.Delete {
		t.Fatalf("a draining instance returned (changed=%v, delete=%v), want both true",
			changed, patch.Delete)
	}
}

func TestAMalformedInstanceRecordWritesNothing(t *testing.T) {
	node := nodeOf("c1-node1")
	node.Spec.ProviderID = "socialweb://previous-life/c1-node1"
	node.Labels = map[string]string{LabelCluster: "previous-life", LabelInstanceType: "small"}
	for _, tc := range []struct {
		name string
		inst Instance
	}{
		{"no cluster", Instance{NodeName: "c1-node1", InstanceType: "small"}},
		{"no node", Instance{Cluster: "c1", InstanceType: "small"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch, changed := Desired(node, tc.inst, true)
			if changed {
				t.Fatalf("a malformed record returned changed=true (patch %+v), so the caller "+
					"would write against a record that names no identity", patch)
			}
			if patch.Delete {
				t.Fatal("a malformed record asked for the Node to be deleted; it is malformed, " +
					"not absent")
			}
			if patch.Reason == "" {
				t.Fatal("a malformed record gave no reason")
			}
		})
	}
}

func TestAnUnstampedNodeIsStamped(t *testing.T) {
	inst := Instance{NodeName: "c1-node1", Cluster: "c1", InstanceType: "small"}
	patch, changed := Desired(nodeOf("c1-node1"), inst, true)
	if !changed {
		t.Fatal("a bare Node returned changed=false, so nothing would ever be written")
	}
	if patch.Delete {
		t.Fatal("a live instance asked for the Node to be deleted")
	}
	if want := "socialweb://c1/c1-node1"; patch.ProviderID != want {
		t.Fatalf("ProviderID = %q, want %q", patch.ProviderID, want)
	}
	if patch.SetLabels[LabelCluster] != "c1" {
		t.Fatalf("cluster label = %q, want c1", patch.SetLabels[LabelCluster])
	}
	if patch.SetLabels[LabelInstanceType] != "small" {
		t.Fatalf("instance-type label = %q, want small", patch.SetLabels[LabelInstanceType])
	}
}

func TestAnAlreadyCorrectNodeIsNotWrittenAgain(t *testing.T) {
	node := nodeOf("c1-node1")
	node.Spec.ProviderID = "socialweb://c1/c1-node1"
	node.Labels = map[string]string{
		LabelCluster:      "c1",
		LabelInstanceType: "small",
	}
	inst := Instance{NodeName: "c1-node1", Cluster: "c1", InstanceType: "small"}
	patch, changed := Desired(node, inst, true)
	if changed {
		t.Fatalf("an already-correct Node returned changed=true (patch %+v), so the caller "+
			"would write when nothing changed", patch)
	}
}

func TestAMissingInstanceTypeRemovesTheLabel(t *testing.T) {
	node := nodeOf("c1-node1")
	node.Spec.ProviderID = "socialweb://c1/c1-node1"
	node.Labels = map[string]string{
		LabelCluster:      "c1",
		LabelInstanceType: "stale",
	}
	inst := Instance{NodeName: "c1-node1", Cluster: "c1", InstanceType: ""}
	patch, changed := Desired(node, inst, true)
	if !changed {
		t.Fatal("a stale instance-type label with no desired value returned changed=false")
	}
	found := false
	for _, k := range patch.RemoveLabels {
		if k == LabelInstanceType {
			found = true
		}
	}
	if !found {
		t.Fatalf("RemoveLabels = %v, want it to hold %q", patch.RemoveLabels, LabelInstanceType)
	}
	if _, set := patch.SetLabels[LabelInstanceType]; set {
		t.Fatal("an empty instance type was still set as a label")
	}
}
