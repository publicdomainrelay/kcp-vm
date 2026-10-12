package trigger

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func triggerObj(spec v1alpha1.RunTriggerSpec, status v1alpha1.RunTriggerStatus) v1alpha1.RunTrigger {
	return v1alpha1.RunTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "watch", Generation: 1},
		Spec:       spec,
		Status:     status,
	}
}

func observedRun(name string, phase v1alpha1.PolicyWorkflowPhase, outputs map[string]string) *RunObservation {
	return &RunObservation{Name: name, Phase: phase, Outputs: outputs}
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

func TestWaitsWhileNoRunHasFinished(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{PolicyWorkflowPod: "open-policy-pod"}, v1alpha1.RunTriggerStatus{}),
		AnyRun:  true,
	})
	if res.Phase != v1alpha1.RunTriggerPending {
		t.Fatalf("phase = %s, want Pending", res.Phase)
	}
}

func TestWaitsWhenNoRunExistsAtAll(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{PolicyWorkflowPod: "open-policy-pod"}, v1alpha1.RunTriggerStatus{}),
	})
	if res.Phase != v1alpha1.RunTriggerPending {
		t.Fatalf("phase = %s, want Pending", res.Phase)
	}
}

func TestMatchingResultCreatesAJob(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{
			PolicyWorkflowPod: "open-policy-pod",
			Match:             map[string]string{"allow": "true"},
		}, v1alpha1.RunTriggerStatus{}),
		Run: observedRun("open-policy-pod-100", v1alpha1.PolicyWorkflowSucceeded, map[string]string{"allow": "true"}),
	})
	if res.Phase != v1alpha1.RunTriggerTriggered || !res.Matched || !hasOp(res, OpCreateJob) {
		t.Fatalf("phase=%s matched=%v ops=%v", res.Phase, res.Matched, res.Ops)
	}
	if res.JobName != "watch-open-policy-pod-100" {
		t.Fatalf("jobName = %q, want watch-open-policy-pod-100", res.JobName)
	}
	if res.LastRun != "open-policy-pod-100" {
		t.Fatalf("lastRun = %q, want open-policy-pod-100", res.LastRun)
	}
}

func TestNonMatchingResultSkips(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{
			PolicyWorkflowPod: "open-policy-pod",
			Match:             map[string]string{"allow": "true"},
		}, v1alpha1.RunTriggerStatus{}),
		Run: observedRun("open-policy-pod-100", v1alpha1.PolicyWorkflowSucceeded, map[string]string{"allow": "false"}),
	})
	if res.Phase != v1alpha1.RunTriggerSkipped || hasOp(res, OpCreateJob) {
		t.Fatalf("phase=%s ops=%v", res.Phase, res.Ops)
	}
}

func TestEmptyMatchTriggersOnSuccess(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{PolicyWorkflowPod: "open-policy-pod"}, v1alpha1.RunTriggerStatus{}),
		Run:     observedRun("open-policy-pod-100", v1alpha1.PolicyWorkflowSucceeded, nil),
	})
	if res.Phase != v1alpha1.RunTriggerTriggered {
		t.Fatalf("phase = %s, want Triggered", res.Phase)
	}
}

func TestTheSameRunDoesNotTriggerTwice(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{PolicyWorkflowPod: "open-policy-pod"},
			v1alpha1.RunTriggerStatus{Phase: v1alpha1.RunTriggerTriggered, JobName: "watch-open-policy-pod-100", LastRun: "open-policy-pod-100"}),
		Run: observedRun("open-policy-pod-100", v1alpha1.PolicyWorkflowSucceeded, nil),
	})
	if hasOp(res, OpCreateJob) {
		t.Fatalf("ops = %v, want no create for an already handled run", res.Ops)
	}
	if res.Phase != v1alpha1.RunTriggerTriggered || res.LastRun != "open-policy-pod-100" {
		t.Fatalf("phase=%s lastRun=%s, want Triggered on open-policy-pod-100", res.Phase, res.LastRun)
	}
}

func TestANewerMatchingRunRefires(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{PolicyWorkflowPod: "open-policy-pod"},
			v1alpha1.RunTriggerStatus{Phase: v1alpha1.RunTriggerTriggered, JobName: "watch-open-policy-pod-100", LastRun: "open-policy-pod-100"}),
		Run: observedRun("open-policy-pod-200", v1alpha1.PolicyWorkflowSucceeded, nil),
	})
	if !hasOp(res, OpCreateJob) {
		t.Fatalf("ops = %v, want a create for a newer run", res.Ops)
	}
	if res.LastRun != "open-policy-pod-200" || res.JobName != "watch-open-policy-pod-200" {
		t.Fatalf("lastRun=%s jobName=%s, want the newer run", res.LastRun, res.JobName)
	}
	if res.Phase != v1alpha1.RunTriggerTriggered || !res.Matched {
		t.Fatalf("phase=%s matched=%v, want Triggered", res.Phase, res.Matched)
	}
}

func TestCancelledRunSkipsWithoutAJob(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{PolicyWorkflowPod: "open-policy-pod"}, v1alpha1.RunTriggerStatus{}),
		Run:     observedRun("open-policy-pod-100", v1alpha1.PolicyWorkflowCancelled, nil),
	})
	if res.Phase != v1alpha1.RunTriggerSkipped || hasOp(res, OpCreateJob) {
		t.Fatalf("phase=%s ops=%v, want Skipped with no job", res.Phase, res.Ops)
	}
}

func TestFailedRunSkips(t *testing.T) {
	res := reconcile(t, Observed{
		Trigger: triggerObj(v1alpha1.RunTriggerSpec{PolicyWorkflowPod: "open-policy-pod"}, v1alpha1.RunTriggerStatus{}),
		Run:     observedRun("open-policy-pod-100", v1alpha1.PolicyWorkflowFailed, nil),
	})
	if res.Phase != v1alpha1.RunTriggerSkipped || hasOp(res, OpCreateJob) {
		t.Fatalf("phase=%s ops=%v, want Skipped with no job", res.Phase, res.Ops)
	}
}

func hasOp(res Result, op Op) bool {
	for _, o := range res.Ops {
		if o == op {
			return true
		}
	}
	return false
}
