//go:build linux

package clusterprovider

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

func observer(cs kubernetes.Interface, err error) NodeObserver {
	return NodeObserver{Client: func(context.Context, string) (kubernetes.Interface, error) {
		return cs, err
	}}
}

func TestObserveCountsWhatKubectlCounts(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
	)
	o := observer(cs, nil)
	obs, count, err := o.Observe(context.Background(), "liveweb")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if count != 2 {
		t.Fatalf("node count = %d, want 2", count)
	}
	if scale.Intent(obs) {
		t.Fatal("an empty cluster reported intent, so a node would be provisioned for nothing")
	}
}

func TestObserveSeesWorkInAnyNamespace(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "someone-elses-ns"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	obs, _, err := observer(cs, nil).Observe(context.Background(), "liveweb")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !scale.Intent(obs) {
		t.Fatal("a running pod in another namespace did not count as intent -- the read is " +
			"scoped to one namespace and the predicate is cluster-wide")
	}
}

func TestObserveCarriesEveryKindThePredicateReads(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "ns"}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "ns"}},
	)
	obs, _, err := observer(cs, nil).Observe(context.Background(), "liveweb")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Pods)+len(obs.Jobs)+len(obs.Deployments)+len(obs.StatefulSets)+len(obs.DaemonSets) != 4 {
		t.Fatalf("observation dropped a kind: %d pods, %d jobs, %d deployments, %d statefulsets, "+
			"%d daemonsets", len(obs.Pods), len(obs.Jobs), len(obs.Deployments),
			len(obs.StatefulSets), len(obs.DaemonSets))
	}
}

func TestAnUnreadableClusterIsAnErrorAndNotAnEmptyObservation(t *testing.T) {
	o := NodeObserver{Client: func(context.Context, string) (kubernetes.Interface, error) {
		return nil, errors.New("the apiserver refused the credential")
	}}
	obs, count, err := o.Observe(context.Background(), "liveweb")
	if err == nil {
		t.Fatal("an unreadable cluster produced no error")
	}
	if count != 0 || scale.Intent(obs) {
		t.Fatalf("an error path returned a usable observation (count=%d): a failure must not be "+
			"readable as an idle cluster", count)
	}
}

func TestAnObserverWithNoClientRefuses(t *testing.T) {
	if _, _, err := (NodeObserver{}).Observe(context.Background(), "liveweb"); err == nil {
		t.Fatal("an observer with no client was allowed to report an observation")
	}
}
