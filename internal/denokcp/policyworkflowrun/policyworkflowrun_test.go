package policyworkflowrun

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

func workflowRun(spec v1alpha1.PolicyWorkflowRunSpec, status v1alpha1.PolicyWorkflowRunStatus) v1alpha1.PolicyWorkflowRun {
	return v1alpha1.PolicyWorkflowRun{
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

func condition(t *testing.T, res Result, conditionType string) metav1.Condition {
	t.Helper()
	for _, c := range res.Conditions {
		if c.Type == conditionType {
			return c
		}
	}
	t.Fatalf("condition %q not found in %+v", conditionType, res.Conditions)
	return metav1.Condition{}
}

func TestAFreshRunStartsExecution(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()}, v1alpha1.PolicyWorkflowRunStatus{}),
		Now:         base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowRunning {
		t.Fatalf("phase = %s, want Running", res.Phase)
	}
	if !hasOp(res, OpStartRun) {
		t.Fatalf("ops = %v, want start-run", res.Ops)
	}
	if res.StartTime == nil {
		t.Fatal("startTime should be set on first start")
	}
}

func TestASuspendedRunDoesNotStart(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), Suspend: true}, v1alpha1.PolicyWorkflowRunStatus{}),
		Now:         base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowPending {
		t.Fatalf("phase = %s, want Pending", res.Phase)
	}
	if hasOp(res, OpStartRun) {
		t.Fatal("a suspended run must not start execution")
	}
	if c := condition(t, res, v1alpha1.ConditionSuspended); c.Status != metav1.ConditionTrue {
		t.Fatalf("Suspended condition = %s, want True", c.Status)
	}
}

func TestASuspendedRunningRunStopsExecution(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), Suspend: true},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1"}),
		Run: &RunObservation{RunID: "r1", State: RunRunning},
		Now: base,
	})
	if !hasOp(res, OpStopRun) {
		t.Fatalf("ops = %v, want stop-run", res.Ops)
	}
	if res.RunID != "" {
		t.Fatalf("runID = %q, want cleared so resume starts fresh", res.RunID)
	}
}

func TestARunningExecutionIsReported(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1"}),
		Run: &RunObservation{RunID: "r1", State: RunRunning},
		Now: base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowRunning || res.Active != 1 {
		t.Fatalf("phase=%s active=%d, want Running/1", res.Phase, res.Active)
	}
}

func TestASucceededExecutionCompletesWithOutputs(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1"}),
		Run: &RunObservation{
			RunID:      "r1",
			State:      RunSucceeded,
			ExitStatus: "success",
			Outputs:    map[string]string{"allow": "true"},
		},
		Now: base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowSucceeded {
		t.Fatalf("phase = %s, want Succeeded", res.Phase)
	}
	if res.ExitStatus != "success" {
		t.Fatalf("exitStatus = %q", res.ExitStatus)
	}
	if res.Outputs["allow"] != "true" {
		t.Fatalf("outputs = %v", res.Outputs)
	}
	if res.CompletionTime == nil {
		t.Fatal("completionTime should be set")
	}
	if c := condition(t, res, v1alpha1.ConditionComplete); c.Status != metav1.ConditionTrue {
		t.Fatalf("Complete condition = %s, want True", c.Status)
	}
}

func TestAFailedExecutionRetriesUntilBackoffLimit(t *testing.T) {
	limit := int32(1)
	spec := v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), BackoffLimit: &limit}

	first := reconcile(t, Observed{
		WorkflowRun: workflowRun(spec, v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1"}),
		Run:         &RunObservation{RunID: "r1", State: RunFailed, ExitStatus: "failure"},
		Now:         base,
	})
	if first.Phase != v1alpha1.PolicyWorkflowPending || first.Retries != 1 {
		t.Fatalf("after first failure: phase=%s retries=%d, want Pending/1", first.Phase, first.Retries)
	}
	if first.RunID != "" {
		t.Fatalf("runID = %q, want cleared so a retry starts", first.RunID)
	}

	second := reconcile(t, Observed{
		WorkflowRun: workflowRun(spec, v1alpha1.PolicyWorkflowRunStatus{
			Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r2", Retries: 1, Failed: 1,
		}),
		Run: &RunObservation{RunID: "r2", State: RunFailed, ExitStatus: "failure"},
		Now: base,
	})
	if second.Phase != v1alpha1.PolicyWorkflowFailed {
		t.Fatalf("after backoffLimit reached: phase=%s, want Failed", second.Phase)
	}
	if condition(t, second, v1alpha1.ConditionFailed).Reason != "BackoffLimitExceeded" {
		t.Fatalf("failed condition = %+v", second.Conditions)
	}
}

func TestBackoffLimitZeroFailsImmediately(t *testing.T) {
	limit := int32(0)
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), BackoffLimit: &limit},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1"}),
		Run: &RunObservation{RunID: "r1", State: RunFailed, ExitStatus: "failure"},
		Now: base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowFailed {
		t.Fatalf("phase = %s, want Failed", res.Phase)
	}
}

func TestActiveDeadlineExceededFails(t *testing.T) {
	deadline := int64(10)
	started := metav1.NewTime(base.Add(-20 * time.Second))
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), ActiveDeadlineSeconds: &deadline},
			v1alpha1.PolicyWorkflowRunStatus{
				Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1", StartTime: &started,
			}),
		Run: &RunObservation{RunID: "r1", State: RunRunning},
		Now: base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowFailed {
		t.Fatalf("phase = %s, want Failed", res.Phase)
	}
	if !hasOp(res, OpStopRun) {
		t.Fatalf("ops = %v, want stop-run", res.Ops)
	}
	if condition(t, res, v1alpha1.ConditionFailed).Reason != "DeadlineExceeded" {
		t.Fatalf("conditions = %+v", res.Conditions)
	}
}

func TestTTLDeletesAfterCompletion(t *testing.T) {
	ttl := int64(30)
	finished := metav1.NewTime(base.Add(-40 * time.Second))
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), TTLSecondsAfterFinished: &ttl},
			v1alpha1.PolicyWorkflowRunStatus{
				Phase: v1alpha1.PolicyWorkflowSucceeded, CompletionTime: &finished,
			}),
		Now: base,
	})
	if !res.Delete || !hasOp(res, OpDelete) {
		t.Fatalf("delete=%v ops=%v, want delete", res.Delete, res.Ops)
	}
	if res.RequeueAfter != RequeueAfter {
		t.Fatalf("requeueAfter = %s, want %s so the driver releases the run's finalizer", res.RequeueAfter, RequeueAfter)
	}
}

func TestTTLRequeuesBeforeExpiry(t *testing.T) {
	ttl := int64(30)
	finished := metav1.NewTime(base.Add(-10 * time.Second))
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), TTLSecondsAfterFinished: &ttl},
			v1alpha1.PolicyWorkflowRunStatus{
				Phase: v1alpha1.PolicyWorkflowSucceeded, CompletionTime: &finished,
			}),
		Now: base,
	})
	if res.Delete {
		t.Fatal("must not delete before the ttl elapses")
	}
	if res.RequeueAfter != 20*time.Second {
		t.Fatalf("requeueAfter = %s, want 20s", res.RequeueAfter)
	}
}

func TestACancelledRunningRunStopsAndCompletes(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), Cancel: true},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1", Active: 1}),
		Run: &RunObservation{RunID: "r1", State: RunRunning},
		Now: base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowCancelled {
		t.Fatalf("phase = %s, want Cancelled", res.Phase)
	}
	if !hasOp(res, OpStopRun) {
		t.Fatalf("ops = %v, want stop-run", res.Ops)
	}
	if !res.RemoveFinalizer || !hasOp(res, OpRemoveFinalizer) {
		t.Fatalf("ops = %v removeFinalizer=%v, want remove-finalizer", res.Ops, res.RemoveFinalizer)
	}
	if res.RunID != "" {
		t.Fatalf("runID = %q, want cleared so polling stops", res.RunID)
	}
	if res.Active != 0 {
		t.Fatalf("active = %d, want 0", res.Active)
	}
	if res.CompletionTime == nil {
		t.Fatal("completionTime should be set on cancel")
	}
	if c := condition(t, res, v1alpha1.ConditionCancelled); c.Status != metav1.ConditionTrue || c.Reason != "Cancelled" {
		t.Fatalf("Cancelled condition = %+v", c)
	}
	if c := condition(t, res, v1alpha1.ConditionCancelled); c.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration = %d, want 1", c.ObservedGeneration)
	}
}

func TestACancelAfterTerminalIsANoOp(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), Cancel: true},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded, Succeeded: 1}),
		Now: base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowSucceeded {
		t.Fatalf("phase = %s, want Succeeded (cancel is a no-op after terminal)", res.Phase)
	}
	if res.RemoveFinalizer || hasOp(res, OpRemoveFinalizer) {
		t.Fatalf("ops = %v, cancel after terminal must not touch the finalizer", res.Ops)
	}
}

func TestDeletionStopsExecutionAndRemovesTheFinalizer(t *testing.T) {
	now := metav1.NewTime(base)
	inst := workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()},
		v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning, RunID: "r1"})
	inst.DeletionTimestamp = &now
	inst.Finalizers = []string{v1alpha1.FinalizerPolicyWorkflowRun}

	res := reconcile(t, Observed{
		WorkflowRun: inst,
		Run:         &RunObservation{RunID: "r1", State: RunRunning},
		Now:         base,
	})
	if !hasOp(res, OpStopRun) || !hasOp(res, OpRemoveFinalizer) {
		t.Fatalf("ops = %v, want stop-run and remove-finalizer", res.Ops)
	}
	if !res.RemoveFinalizer {
		t.Fatal("RemoveFinalizer should be true")
	}
	if res.Phase != v1alpha1.PolicyWorkflowCancelled {
		t.Fatalf("phase = %s, want Cancelled on deletion", res.Phase)
	}
	if !hasCondition(res, v1alpha1.ConditionCancelled) {
		t.Fatalf("conditions = %+v, want a Cancelled condition", res.Conditions)
	}
}

func TestAQueuedRunStaysPendingWithoutStarting(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()}, v1alpha1.PolicyWorkflowRunStatus{}),
		Admission:   &Admission{Allowed: false, Reason: "AtCapacity", Message: "waiting for a free slot"},
		Now:         base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowPending {
		t.Fatalf("phase = %s, want Pending", res.Phase)
	}
	if hasOp(res, OpStartRun) {
		t.Fatal("a queued run must not start execution")
	}
	if res.StartTime != nil {
		t.Fatal("a queued run must not record a start time")
	}
	if c := condition(t, res, v1alpha1.ConditionComplete); c.Status != metav1.ConditionFalse || c.Reason != "AtCapacity" {
		t.Fatalf("Complete condition = %+v", c)
	}
}

func TestAnAdmittedRunStarts(t *testing.T) {
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()}, v1alpha1.PolicyWorkflowRunStatus{}),
		Admission:   &Admission{Allowed: true},
		Now:         base,
	})
	if res.Phase != v1alpha1.PolicyWorkflowRunning || !hasOp(res, OpStartRun) {
		t.Fatalf("phase=%s ops=%v, want Running/start-run", res.Phase, res.Ops)
	}
}

func TestEffectiveTTLAppliesWhenTheSpecHasNone(t *testing.T) {
	ttl := int64(30)
	finished := metav1.NewTime(base.Add(-40 * time.Second))
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded, CompletionTime: &finished}),
		EffectiveTTLSeconds: &ttl,
		Now:                 base,
	})
	if !res.Delete || !hasOp(res, OpDelete) {
		t.Fatalf("delete=%v ops=%v, want the effective ttl to reap the run", res.Delete, res.Ops)
	}
}

func TestSpecTTLWinsOverTheEffectiveTTL(t *testing.T) {
	effective := int64(300)
	spec := int64(90)
	finished := metav1.NewTime(base.Add(-10 * time.Second))
	res := reconcile(t, Observed{
		WorkflowRun: workflowRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), TTLSecondsAfterFinished: &spec},
			v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded, CompletionTime: &finished}),
		EffectiveTTLSeconds: &effective,
		Now:                 base,
	})
	if res.Delete {
		t.Fatal("the spec ttl has not elapsed; the run must not be reaped")
	}
	if res.RequeueAfter != 80*time.Second {
		t.Fatalf("requeueAfter = %s, want the spec ttl's 80s", res.RequeueAfter)
	}
}

func hasCondition(res Result, conditionType string) bool {
	for _, c := range res.Conditions {
		if c.Type == conditionType {
			return true
		}
	}
	return false
}
