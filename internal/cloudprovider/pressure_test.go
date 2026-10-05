package cloudprovider

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func quantity(s string) resource.Quantity {
	return resource.MustParse(s)
}

func pendingPod(name string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
}

func scheduledPendingPod(name string) corev1.Pod {
	p := pendingPod(name)
	p.Spec.NodeName = "c1-node1"
	return p
}

func runningPod(name string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func terminalPod(name string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func readyNode(name, pods, cpu, memory string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourcePods:   quantity(pods),
				corev1.ResourceCPU:    quantity(cpu),
				corev1.ResourceMemory: quantity(memory),
			},
		},
	}
}

func TestMeasureCountsOnlyUnscheduledPendingAsPending(t *testing.T) {
	p := Measure([]corev1.Pod{
		pendingPod("unscheduled"),
		scheduledPendingPod("already-placed"),
		runningPod("running"),
	}, nil)
	if p.Pending != 1 {
		t.Fatalf("Pending = %d, want 1: only a Pending pod with no node is a request for a new "+
			"node; a Pending pod already placed has one", p.Pending)
	}
	if p.Running != 1 {
		t.Fatalf("Running = %d, want 1", p.Running)
	}
}

func TestMeasureIgnoresControlPlaneOwnedPods(t *testing.T) {
	owned := runningPod("coredns")
	owned.Annotations = map[string]string{
		"objectset.rio.cattle.io/owner-gvk": "k3s.cattle.io/v1, Kind=Addon",
	}
	p := Measure([]corev1.Pod{owned, pendingPod("real-work")}, nil)
	if p.Running != 0 {
		t.Fatalf("Running = %d, want 0: a pod the control plane applied is not user work", p.Running)
	}
	if p.Pending != 1 {
		t.Fatalf("Pending = %d, want 1", p.Pending)
	}
}

func TestMeasureIgnoresTerminalPods(t *testing.T) {
	p := Measure([]corev1.Pod{
		terminalPod("done", corev1.PodSucceeded),
		terminalPod("failed", corev1.PodFailed),
	}, nil)
	if p.Pending != 0 || p.Running != 0 {
		t.Fatalf("terminal pods counted: Pending=%d Running=%d, want 0/0", p.Pending, p.Running)
	}
}

func TestMeasureAllocatableIsInMilliCPUsAndBytes(t *testing.T) {
	nodes := []corev1.Node{
		readyNode("c1-node1", "110", "2", "1Gi"),
		readyNode("c1-node2", "110", "500m", "512Mi"),
	}
	notReady := readyNode("c1-node3", "110", "4", "1Gi")
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	nodes = append(nodes, notReady)

	p := Measure(nil, nodes)
	if p.Nodes != 3 {
		t.Fatalf("Nodes = %d, want 3: every Node the cluster knows is counted", p.Nodes)
	}
	if want := 2000 + 500; p.AllocatableCPUMilli != int64(want) {
		t.Fatalf("AllocatableCPUMilli = %d, want %d: CPU is in millicores, so 500m is 500 and "+
			"2 is 2000", p.AllocatableCPUMilli, want)
	}
	if want := int64(1<<30 + 512<<20); p.AllocatableMemoryBytes != want {
		t.Fatalf("AllocatableMemoryBytes = %d, want %d: memory is in bytes, so 1Gi is 1073741824 "+
			"and 512Mi is 536870912", p.AllocatableMemoryBytes, want)
	}
	if p.AllocatablePods != 220 {
		t.Fatalf("AllocatablePods = %d, want 220: the NotReady node's 110 pods are not allocatable",
			p.AllocatablePods)
	}
}

func TestTargetIsZeroWithNoWork(t *testing.T) {
	if got := Target(Pressure{}, 0, 4, 8); got != 0 {
		t.Fatalf("Target with no work = %d, want 0", got)
	}
}

func TestTargetRoundsUpPastOneNode(t *testing.T) {
	if got := Target(Pressure{Running: 1}, 0, 4, 8); got != 1 {
		t.Fatalf("Target with one pod = %d, want 1", got)
	}
	if got := Target(Pressure{Running: 9}, 0, 4, 8); got != 2 {
		t.Fatalf("Target with nine pods and eight per node = %d, want 2", got)
	}
	if got := Target(Pressure{Pending: 8, Running: 8}, 0, 4, 8); got != 2 {
		t.Fatalf("Target with sixteen pods and eight per node = %d, want 2", got)
	}
}

func TestTargetCapsAtMaxNodes(t *testing.T) {
	if got := Target(Pressure{Running: 1000}, 0, 4, 8); got != 4 {
		t.Fatalf("Target with 1000 pods and a ceiling of 4 = %d, want 4", got)
	}
}

func TestTargetNeverFallsBelowOne(t *testing.T) {
	if got := Target(Pressure{Pending: 1}, 0, 1, 8); got != 1 {
		t.Fatalf("Target = %d, want 1", got)
	}
}

func TestTargetHoldsWhenTheCeilingIsUnresolved(t *testing.T) {
	if got := Target(Pressure{Running: 1000}, 2, 0, 8); got != 2 {
		t.Fatalf("Target with maxNodes=0 = %d, want the current 2 and NOT a scale-up: an "+
			"unresolved ceiling is not an unlimited one, so the caller must resolve it and until "+
			"it does no decision is taken", got)
	}
}

func TestTargetHoldsWhenThereIsNoRatio(t *testing.T) {
	if got := Target(Pressure{Running: 1000}, 2, 4, 0); got != 2 {
		t.Fatalf("Target with podsPerNode=0 = %d, want the current 2: no ratio is computable, so "+
			"nothing is invented", got)
	}
}
