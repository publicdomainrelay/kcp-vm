package clusterprovider

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

func one() *int32 { v := int32(1); return &v }

func managedBy(manager string, op metav1.ManagedFieldsOperationType) []metav1.ManagedFieldsEntry {
	return []metav1.ManagedFieldsEntry{{Manager: manager, Operation: op, APIVersion: "apps/v1"}}
}

func controller(kind, name string) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &yes}}
}

func installedESO() []runtime.Object {
	var objs []runtime.Object
	for _, name := range []string{"external-secrets", "external-secrets-cert-controller", "external-secrets-webhook"} {
		objs = append(objs,
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name,
					ManagedFields: managedBy(AddOnFieldManager, metav1.ManagedFieldsOperationApply)},
				Spec: appsv1.DeploymentSpec{Replicas: one()},
			},
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name + "-7f9c",
				OwnerReferences: controller(scale.KindDeployment, name)}},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name + "-7f9c-x2",
					OwnerReferences: controller(scale.KindReplicaSet, name+"-7f9c")},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
		)
	}
	return objs
}

func baselineObserver(cs kubernetes.Interface, b *scale.Baseline) NodeObserver {
	return NodeObserver{
		Client: func(context.Context, string) (kubernetes.Interface, error) { return cs, nil },
		Baseline: func(string) (scale.Baseline, error) {
			if b == nil {
				return scale.Baseline{}, nil
			}
			return *b, nil
		},
	}
}

func TestTheBaselineIsWhatOurFieldManagerApplied(t *testing.T) {
	objs := append(installedESO(),
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web",
				ManagedFields: managedBy("kubectl", metav1.ManagedFieldsOperationApply)},
			Spec: appsv1.DeploymentSpec{Replicas: one()},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "edited",
				ManagedFields: managedBy(AddOnFieldManager, metav1.ManagedFieldsOperationUpdate)},
			Spec: appsv1.DeploymentSpec{Replicas: one()},
		},
	)
	cs := fake.NewSimpleClientset(objs...)
	b, err := MeasureAddOnBaseline(context.Background(), cs, AddOnFieldManager, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Workloads) != 3 {
		t.Fatalf("measured %d workloads (%v), want the three our installer applied: a tenant's apply and "+
			"a non-apply write under our name are not our install", len(b.Workloads), b.Workloads)
	}
	for _, e := range b.Workloads {
		if e.Kind != scale.KindDeployment || e.Namespace != "default" || e.SpecHash == "" {
			t.Fatalf("baseline entry %+v", e)
		}
	}
}

func TestObserveLeavesAnIdleClusterOfOurAddOnsAtNoIntent(t *testing.T) {
	cs := fake.NewSimpleClientset(installedESO()...)
	without, _, err := baselineObserver(cs, nil).Observe(context.Background(), "liveweb")
	if err != nil {
		t.Fatal(err)
	}
	if got := scale.Demand(without); got != 3 {
		t.Fatalf("with no baseline the three Pending operator pods and their Deployments demanded %d, want 3", got)
	}
	b, err := MeasureAddOnBaseline(context.Background(), cs, AddOnFieldManager, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	with, _, err := baselineObserver(cs, &b).Observe(context.Background(), "liveweb")
	if err != nil {
		t.Fatal(err)
	}
	if scale.Intent(with) {
		t.Fatalf("a cluster holding only the add-ons we installed reported intent (demand %d)", scale.Demand(with))
	}
}

func TestTheBaselineSurvivesARoundTripThroughItsFile(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := ReadAddOnBaseline(dir); ok || err != nil {
		t.Fatalf("an unmeasured cluster read as measured=%v err=%v", ok, err)
	}
	cs := fake.NewSimpleClientset(installedESO()...)
	b, err := MeasureAddOnBaseline(context.Background(), cs, AddOnFieldManager, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAddOnBaseline(dir, b); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadAddOnBaseline(dir)
	if err != nil || !ok {
		t.Fatalf("read back measured=%v err=%v", ok, err)
	}
	if len(got.Workloads) != 3 || got.Workloads[0] != b.Workloads[0] || !got.MeasuredAt.Equal(b.MeasuredAt) {
		t.Fatalf("read back %+v, wrote %+v", got, b)
	}
}
