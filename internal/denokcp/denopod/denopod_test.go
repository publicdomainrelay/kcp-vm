package denopod

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func pod(spec v1alpha1.DenoPodSpec, status v1alpha1.DenoPodStatus) v1alpha1.DenoPod {
	return v1alpha1.DenoPod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Generation: 1},
		Spec:       spec,
		Status:     status,
	}
}

func tmpl() v1alpha1.DenoPodTemplate {
	return v1alpha1.DenoPodTemplate{Script: "x"}
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

func TestFreshPodStarts(t *testing.T) {
	res := reconcile(t, Observed{Pod: pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl()}, v1alpha1.DenoPodStatus{})})
	if res.Phase != v1alpha1.DenoPodRunning || !hasOp(res, OpStartRun) {
		t.Fatalf("phase=%s ops=%v", res.Phase, res.Ops)
	}
	if res.StartTime == nil {
		t.Fatal("startTime should be set")
	}
}

func TestRunningPodReportsReadiness(t *testing.T) {
	p := pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl()}, v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1"})
	passed := true
	res := reconcile(t, Observed{Pod: p, Execution: &ExecutionObservation{RunID: "r1", State: ExecutionRunning}, ReadinessPassed: &passed})
	if res.Phase != v1alpha1.DenoPodRunning || !res.Ready {
		t.Fatalf("phase=%s ready=%v", res.Phase, res.Ready)
	}
}

func TestNeverRestartsWithExitZeroSucceeds(t *testing.T) {
	code := int32(0)
	p := pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl(), RestartPolicy: v1alpha1.RestartNever},
		v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1"})
	res := reconcile(t, Observed{Pod: p, Execution: &ExecutionObservation{RunID: "r1", State: ExecutionExited, ExitCode: &code}})
	if res.Phase != v1alpha1.DenoPodSucceeded {
		t.Fatalf("phase=%s, want Succeeded", res.Phase)
	}
	if res.CompletionTime == nil {
		t.Fatal("completionTime should be set")
	}
}

func TestNeverWithNonZeroExitFails(t *testing.T) {
	code := int32(2)
	p := pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl(), RestartPolicy: v1alpha1.RestartNever},
		v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1"})
	res := reconcile(t, Observed{Pod: p, Execution: &ExecutionObservation{RunID: "r1", State: ExecutionExited, ExitCode: &code}})
	if res.Phase != v1alpha1.DenoPodFailed {
		t.Fatalf("phase=%s, want Failed", res.Phase)
	}
}

func TestAlwaysRestartsOnExit(t *testing.T) {
	code := int32(0)
	p := pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl()}, v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1"})
	res := reconcile(t, Observed{Pod: p, Execution: &ExecutionObservation{RunID: "r1", State: ExecutionExited, ExitCode: &code}})
	if res.Phase != v1alpha1.DenoPodRunning || res.Restarts != 1 || res.RunID != "" {
		t.Fatalf("phase=%s restarts=%d runID=%q", res.Phase, res.Restarts, res.RunID)
	}
	if res.CompletionTime != nil {
		t.Fatal("a restarted pod is not complete")
	}
}

func TestOnFailureRestartsOnlyOnFailure(t *testing.T) {
	code := int32(3)
	p := pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl(), RestartPolicy: v1alpha1.RestartOnFailure},
		v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1"})
	res := reconcile(t, Observed{Pod: p, Execution: &ExecutionObservation{RunID: "r1", State: ExecutionExited, ExitCode: &code}})
	if res.Phase != v1alpha1.DenoPodRunning || res.Restarts != 1 {
		t.Fatalf("phase=%s restarts=%d", res.Phase, res.Restarts)
	}
}

func TestLivenessFailureRestarts(t *testing.T) {
	p := pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl()}, v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1"})
	res := reconcile(t, Observed{
		Pod:            p,
		Execution:      &ExecutionObservation{RunID: "r1", State: ExecutionRunning},
		LivenessFailed: true,
	})
	if res.Restarts != 1 || !hasOp(res, OpStopRun) || res.RunID != "" || res.Ready {
		t.Fatalf("restarts=%d ops=%v runID=%q ready=%v", res.Restarts, res.Ops, res.RunID, res.Ready)
	}
}

func TestDeadlineStopsAndFails(t *testing.T) {
	deadline := int64(10)
	started := metav1.NewTime(base.Add(-20 * time.Second))
	res := reconcile(t, Observed{
		Pod: pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl(), ActiveDeadlineSeconds: &deadline},
			v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1", StartTime: &started}),
		Execution: &ExecutionObservation{RunID: "r1", State: ExecutionRunning},
	})
	if res.Phase != v1alpha1.DenoPodFailed || !hasOp(res, OpStopRun) {
		t.Fatalf("phase=%s ops=%v", res.Phase, res.Ops)
	}
}

func TestTTLDeletes(t *testing.T) {
	ttl := int64(30)
	finished := metav1.NewTime(base.Add(-40 * time.Second))
	res := reconcile(t, Observed{
		Pod: pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl(), TTLSecondsAfterFinished: &ttl},
			v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodFailed, CompletionTime: &finished}),
	})
	if !res.Delete || !hasOp(res, OpDelete) {
		t.Fatalf("delete=%v ops=%v", res.Delete, res.Ops)
	}
}

func TestDeletionStopsAndUnfinalizes(t *testing.T) {
	now := metav1.NewTime(base)
	inst := pod(v1alpha1.DenoPodSpec{DenoPodTemplate: tmpl()}, v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, RunID: "r1"})
	inst.DeletionTimestamp = &now
	inst.Finalizers = []string{v1alpha1.FinalizerDenoPod}
	res := reconcile(t, Observed{Pod: inst, Execution: &ExecutionObservation{RunID: "r1", State: ExecutionRunning}})
	if !hasOp(res, OpStopRun) || !hasOp(res, OpRemoveFinalizer) || !res.RemoveFinalizer {
		t.Fatalf("ops=%v removeFinalizer=%v", res.Ops, res.RemoveFinalizer)
	}
}
