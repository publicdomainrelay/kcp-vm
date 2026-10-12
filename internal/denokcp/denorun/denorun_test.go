package denorun

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func run(spec v1alpha1.DenoRunSpec, status v1alpha1.DenoRunStatus) v1alpha1.DenoRun {
	return v1alpha1.DenoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Generation: 1},
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

func TestFreshRunStarts(t *testing.T) {
	res := reconcile(t, Observed{Run: run(v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}, v1alpha1.DenoRunStatus{})})
	if res.Phase != v1alpha1.DenoRunRunning || !hasOp(res, OpStartRun) {
		t.Fatalf("phase=%s ops=%v", res.Phase, res.Ops)
	}
}

func TestSucceededRunReportsOutputs(t *testing.T) {
	code := int32(0)
	res := reconcile(t, Observed{
		Run:       run(v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}, v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunRunning, RunID: "r1"}),
		Execution: &RunObservation{RunID: "r1", State: RunSucceeded, ExitCode: &code, Outputs: map[string]string{"allow": "true"}},
	})
	if res.Phase != v1alpha1.DenoRunSucceeded || res.Outputs["allow"] != "true" || res.CompletionTime == nil {
		t.Fatalf("phase=%s outputs=%v", res.Phase, res.Outputs)
	}
}

func TestFailedRunWithoutBackoffIsTerminal(t *testing.T) {
	code := int32(2)
	res := reconcile(t, Observed{
		Run:       run(v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}, v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunRunning, RunID: "r1"}),
		Execution: &RunObservation{RunID: "r1", State: RunFailed, ExitCode: &code, Message: "boom"},
	})
	if res.Phase != v1alpha1.DenoRunFailed || res.Message != "boom" {
		t.Fatalf("phase=%s message=%q", res.Phase, res.Message)
	}
}

func TestFailedRunRetriesUpToBackoff(t *testing.T) {
	backoff := int32(1)
	spec := v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}, Backoff: &backoff}
	first := reconcile(t, Observed{
		Run:       run(spec, v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunRunning, RunID: "r1"}),
		Execution: &RunObservation{RunID: "r1", State: RunFailed},
	})
	if first.Phase != v1alpha1.DenoRunPending || first.Retries != 1 || first.RunID != "" {
		t.Fatalf("first: phase=%s retries=%d runID=%q", first.Phase, first.Retries, first.RunID)
	}
	second := reconcile(t, Observed{
		Run:       run(spec, v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunRunning, RunID: "r2", Retries: 1}),
		Execution: &RunObservation{RunID: "r2", State: RunFailed},
	})
	if second.Phase != v1alpha1.DenoRunFailed {
		t.Fatalf("second: phase=%s, want Failed", second.Phase)
	}
}

// ponytail: the contract the provider reads. A cleared RunID reaches status.runID only if the decider says it dropped one, so this drives Reconcile the way reconcileRun does and counts attempts rather than passes: spec.backoff N is N+1 attempts, then Failed.
func TestBackoffMeansOneAttemptPerRetry(t *testing.T) {
	for _, backoff := range []int32{0, 1, 2, 3} {
		limit := backoff
		spec := v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}, Backoff: &limit}
		var status v1alpha1.DenoRunStatus
		attempts := 0
		for pass := 1; pass <= 2*(int(backoff)+1); pass++ {
			observed := Observed{Run: run(spec, status)}
			if status.RunID != "" {
				observed.Execution = &RunObservation{RunID: status.RunID, State: RunFailed}
			}
			res := reconcile(t, observed)
			if observed.Execution != nil && !res.RunIDCleared {
				t.Fatalf("backoff %d pass %d: a failed observation has to clear RunID", backoff, pass)
			}
			status.Phase = res.Phase
			status.Retries = res.Retries
			if hasOp(res, OpStartRun) {
				if res.RunIDCleared {
					t.Fatalf("backoff %d pass %d: a start has no workload to drop", backoff, pass)
				}
				attempts++
				status.RunID = fmt.Sprintf("r%d", attempts)
				status.StartTime = res.StartTime
				continue
			}
			status.RunID = res.RunID
		}
		if attempts != int(backoff)+1 {
			t.Fatalf("backoff %d: attempts = %d, want %d", backoff, attempts, backoff+1)
		}
		if status.Phase != v1alpha1.DenoRunFailed {
			t.Fatalf("backoff %d: phase = %s, want Failed", backoff, status.Phase)
		}
		if status.Retries != int32(attempts) {
			t.Fatalf("backoff %d: retries = %d, want %d, one per attempt", backoff, status.Retries, attempts)
		}
	}
}

func TestTTLDeletes(t *testing.T) {
	ttl := int64(30)
	finished := metav1.NewTime(base.Add(-40 * time.Second))
	res := reconcile(t, Observed{
		Run: run(v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}, TTLSecondsAfterFinished: &ttl},
			v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunSucceeded, CompletionTime: &finished}),
	})
	if !res.Delete || !hasOp(res, OpDelete) {
		t.Fatalf("delete=%v ops=%v", res.Delete, res.Ops)
	}
	if res.RequeueAfter != RequeueAfter {
		t.Fatalf("requeueAfter = %s, want %s so the driver releases the run's finalizer", res.RequeueAfter, RequeueAfter)
	}
}

func TestDeletionStopsAndUnfinalizes(t *testing.T) {
	now := metav1.NewTime(base)
	obj := run(v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}, v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunRunning, RunID: "r1"})
	obj.DeletionTimestamp = &now
	obj.Finalizers = []string{v1alpha1.FinalizerDenoRun}
	res := reconcile(t, Observed{Run: obj, Execution: &RunObservation{RunID: "r1", State: RunRunning}})
	if !hasOp(res, OpStopRun) || !hasOp(res, OpRemoveFinalizer) || !res.RemoveFinalizer {
		t.Fatalf("ops=%v removeFinalizer=%v", res.Ops, res.RemoveFinalizer)
	}
}
