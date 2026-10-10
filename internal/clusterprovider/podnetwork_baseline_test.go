package clusterprovider

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantnet"
)

func TestThePodNetworkAddOnsAreBaselineAndNeverIntent(t *testing.T) {
	controller := true
	cs := fake.NewSimpleClientset(
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: tenantnet.NodeReaderName,
			ManagedFields: managedBy(AddOnFieldManager, metav1.ManagedFieldsOperationApply)}},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: tenantnet.DNSName,
				ManagedFields: managedBy(AddOnFieldManager, metav1.ManagedFieldsOperationApply)},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				HostNetwork:  true,
				NodeSelector: map[string]string{tenantnet.NodeLabel: "true"},
			}}},
		},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "liveweb-node1",
			Labels: map[string]string{tenantnet.NodeLabel: "true"}}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: tenantnet.DNSName + "-x2",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: scale.KindDaemonSet,
					Name: tenantnet.DNSName, Controller: &controller}}},
			Spec:   corev1.PodSpec{NodeName: "liveweb-node1", HostNetwork: true},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	b, err := MeasureAddOnBaseline(context.Background(), cs, AddOnFieldManager, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := scale.WorkloadKey{Kind: scale.KindDaemonSet, Namespace: "kube-system", Name: tenantnet.DNSName}
	if len(b.Workloads) != 1 || b.Workloads[0].WorkloadKey != want {
		t.Fatalf("baseline %+v, want exactly the per-node DNS DaemonSet our add-on manager applied", b.Workloads)
	}
	obs, _, err := baselineObserver(cs, &b).Observe(context.Background(), "liveweb")
	if err != nil {
		t.Fatal(err)
	}
	if scale.Intent(obs) || scale.Demand(obs) != 0 {
		t.Fatalf("a node running only the per-node DNS reported intent %v demand %d: the node network is "+
			"ours, so it never holds a node", scale.Intent(obs), scale.Demand(obs))
	}
	if load, ok := obs.Placement.Load("liveweb-node1"); !ok || load.Busy != 0 {
		t.Fatalf("placement %+v: the per-node DNS pod must leave its node idle for scale-in", obs.Placement)
	}
}
