package policyengine

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func engine(spec v1alpha1.PolicyEngineSpec, status v1alpha1.PolicyEngineStatus) v1alpha1.PolicyEngine {
	return v1alpha1.PolicyEngine{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-engine", Generation: 1},
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

func hasOp(res Result, op Op) bool {
	for _, o := range res.Ops {
		if o == op {
			return true
		}
	}
	return false
}

func TestFreshEngineStarts(t *testing.T) {
	res := reconcile(t, Observed{Engine: engine(v1alpha1.PolicyEngineSpec{}, v1alpha1.PolicyEngineStatus{}), Endpoint: "http://127.0.0.1:5111"})
	if res.Phase != v1alpha1.PolicyEngineRunning || !hasOp(res, OpStartEngine) {
		t.Fatalf("phase=%s ops=%v", res.Phase, res.Ops)
	}
	if res.Endpoint != "http://127.0.0.1:5111" {
		t.Fatalf("endpoint = %q", res.Endpoint)
	}
}

func TestRunningEngineReportsReadiness(t *testing.T) {
	passed := true
	res := reconcile(t, Observed{
		Engine:          engine(v1alpha1.PolicyEngineSpec{}, v1alpha1.PolicyEngineStatus{Phase: v1alpha1.PolicyEngineRunning, RunID: "e1", Endpoint: "http://127.0.0.1:5111"}),
		Execution:       &ExecutionObservation{RunID: "e1", State: ExecutionRunning},
		ReadinessPassed: &passed,
	})
	if res.Phase != v1alpha1.PolicyEngineRunning || !res.Ready {
		t.Fatalf("phase=%s ready=%v", res.Phase, res.Ready)
	}
	if res.Endpoint != "http://127.0.0.1:5111" {
		t.Fatalf("endpoint = %q", res.Endpoint)
	}
}

func TestExitedEngineRestartsByDefault(t *testing.T) {
	res := reconcile(t, Observed{
		Engine:    engine(v1alpha1.PolicyEngineSpec{}, v1alpha1.PolicyEngineStatus{Phase: v1alpha1.PolicyEngineRunning, RunID: "e1", Endpoint: "http://127.0.0.1:5111"}),
		Execution: &ExecutionObservation{RunID: "e1", State: ExecutionExited},
	})
	if res.Phase != v1alpha1.PolicyEngineRunning || res.Restarts != 1 || res.RunID != "" || res.Ready {
		t.Fatalf("phase=%s restarts=%d runID=%q ready=%v", res.Phase, res.Restarts, res.RunID, res.Ready)
	}
}

func TestExitedEngineWithNeverFails(t *testing.T) {
	res := reconcile(t, Observed{
		Engine:    engine(v1alpha1.PolicyEngineSpec{RestartPolicy: v1alpha1.RestartNever}, v1alpha1.PolicyEngineStatus{Phase: v1alpha1.PolicyEngineRunning, RunID: "e1"}),
		Execution: &ExecutionObservation{RunID: "e1", State: ExecutionExited, Message: "boom"},
	})
	if res.Phase != v1alpha1.PolicyEngineFailed {
		t.Fatalf("phase=%s, want Failed", res.Phase)
	}
}

func TestLivenessFailureRestarts(t *testing.T) {
	res := reconcile(t, Observed{
		Engine:         engine(v1alpha1.PolicyEngineSpec{}, v1alpha1.PolicyEngineStatus{Phase: v1alpha1.PolicyEngineRunning, RunID: "e1", Endpoint: "http://127.0.0.1:5111"}),
		Execution:      &ExecutionObservation{RunID: "e1", State: ExecutionRunning},
		LivenessFailed: true,
	})
	if res.Restarts != 1 || !hasOp(res, OpStopEngine) {
		t.Fatalf("restarts=%d ops=%v", res.Restarts, res.Ops)
	}
}

func TestDeletionStopsAndUnfinalizes(t *testing.T) {
	now := metav1.NewTime(base)
	obj := engine(v1alpha1.PolicyEngineSpec{}, v1alpha1.PolicyEngineStatus{Phase: v1alpha1.PolicyEngineRunning, RunID: "e1"})
	obj.DeletionTimestamp = &now
	obj.Finalizers = []string{v1alpha1.FinalizerPolicyEngine}
	res := reconcile(t, Observed{Engine: obj, Execution: &ExecutionObservation{RunID: "e1", State: ExecutionRunning}})
	if !hasOp(res, OpStopEngine) || !hasOp(res, OpRemoveFinalizer) || !res.RemoveFinalizer {
		t.Fatalf("ops=%v removeFinalizer=%v", res.Ops, res.RemoveFinalizer)
	}
}
