package policyworkflowpod

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func rawWorkflow() runtime.RawExtension {
	return runtime.RawExtension{Raw: []byte(`{"name":"x"}`)}
}

func pod(spec v1alpha1.PolicyWorkflowPodSpec, status v1alpha1.PolicyWorkflowPodStatus) v1alpha1.PolicyWorkflowPod {
	return v1alpha1.PolicyWorkflowPod{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod", Generation: 1},
		Spec:       spec,
		Status:     status,
	}
}

func reconcile(t *testing.T, o Observed) Result {
	t.Helper()
	r := New(Options{Now: func() time.Time { return base }})
	res, err := r.Reconcile(context.Background(), o)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func TestPendingUntilEngineReady(t *testing.T) {
	res := reconcile(t, Observed{Pod: pod(v1alpha1.PolicyWorkflowPodSpec{Workflow: rawWorkflow()}, v1alpha1.PolicyWorkflowPodStatus{})})
	if res.Phase != v1alpha1.PolicyWorkflowPodPending {
		t.Fatalf("phase = %s, want Pending", res.Phase)
	}
}

func TestRunningWhenEngineReady(t *testing.T) {
	res := reconcile(t, Observed{
		Pod:            pod(v1alpha1.PolicyWorkflowPodSpec{Workflow: rawWorkflow()}, v1alpha1.PolicyWorkflowPodStatus{}),
		EngineReady:    true,
		EngineEndpoint: "http://127.0.0.1:5111",
	})
	if res.Phase != v1alpha1.PolicyWorkflowPodRunning {
		t.Fatalf("phase = %s, want Running", res.Phase)
	}
	if res.Endpoint != "http://127.0.0.1:5111" {
		t.Fatalf("endpoint=%q", res.Endpoint)
	}
}

func i64(v int64) *int64 { return &v }

func TestRunTTLResolution(t *testing.T) {
	cases := []struct {
		name     string
		podTTL   *int64
		provider *int64
		want     *int64
	}{
		{"pod wins", i64(30), i64(3600), i64(30)},
		{"pod nil uses provider", nil, i64(3600), i64(3600)},
		{"both nil", nil, nil, nil},
		{"negative default", nil, i64(-1), nil},
		{"negative pod", i64(-5), i64(3600), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := pod(v1alpha1.PolicyWorkflowPodSpec{RunTTLSecondsAfterFinished: tc.podTTL}, v1alpha1.PolicyWorkflowPodStatus{})
			got := RunTTL(&p, tc.provider)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("RunTTL = %v, want %v", got, tc.want)
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("RunTTL = %d, want %d", *got, *tc.want)
			}
		})
	}
}

func TestRunTTLNilPodUsesProviderDefault(t *testing.T) {
	if got := RunTTL(nil, i64(600)); got == nil || *got != 600 {
		t.Fatalf("RunTTL(nil) = %v, want 600", got)
	}
}
