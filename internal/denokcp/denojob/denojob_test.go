package denojob

import (
	"context"
	"strconv"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func job(spec v1alpha1.DenoJobSpec, status v1alpha1.DenoJobStatus) v1alpha1.DenoJob {
	return v1alpha1.DenoJob{
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

func run(name string, phase v1alpha1.DenoRunPhase) RunObservation {
	return RunObservation{Name: name, Phase: phase}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func apply(res Result, runs []RunObservation) []RunObservation {
	out := make([]RunObservation, 0, len(runs)+len(res.CreateRuns))
	for _, r := range runs {
		if containsName(res.StopRuns, r.Name) {
			continue
		}
		out = append(out, r)
	}
	for _, name := range res.CreateRuns {
		out = append(out, run(name, v1alpha1.DenoRunPending))
	}
	return out
}

func finishPending(runs []RunObservation) int {
	started := 0
	for k := range runs {
		if runs[k].Phase != v1alpha1.DenoRunSucceeded && runs[k].Phase != v1alpha1.DenoRunFailed {
			runs[k].Phase = v1alpha1.DenoRunSucceeded
			started++
		}
	}
	return started
}

func TestJobCreatesARun(t *testing.T) {
	res := reconcile(t, Observed{Job: job(v1alpha1.DenoJobSpec{}, v1alpha1.DenoJobStatus{})})
	if res.Phase != v1alpha1.DenoJobPending || len(res.CreateRuns) != 1 {
		t.Fatalf("phase=%s create=%v", res.Phase, res.CreateRuns)
	}
	if res.RunName != "demo-1" || res.CreateRuns[0] != "demo-1" {
		t.Fatalf("runName=%q create=%v, want demo-1", res.RunName, res.CreateRuns)
	}
	if res.Active != 1 {
		t.Fatalf("active = %d, want 1", res.Active)
	}
}

func TestJobSucceedsWhenItsRunDoes(t *testing.T) {
	code := int32(0)
	res := reconcile(t, Observed{
		Job: job(v1alpha1.DenoJobSpec{}, v1alpha1.DenoJobStatus{Phase: v1alpha1.DenoJobRunning, RunName: "demo-1"}),
		Runs: []RunObservation{
			{Name: "demo-1", Phase: v1alpha1.DenoRunSucceeded, ExitCode: &code, Outputs: map[string]string{"allow": "true"}},
		},
	})
	if res.Phase != v1alpha1.DenoJobSucceeded || res.Outputs["allow"] != "true" || res.Succeeded != 1 {
		t.Fatalf("phase=%s succeeded=%d outputs=%v", res.Phase, res.Succeeded, res.Outputs)
	}
}

func TestParallelismStartsAtMostParallelismAndCompletesAtCompletions(t *testing.T) {
	completions := int32(5)
	parallelism := int32(2)
	spec := v1alpha1.DenoJobSpec{Completions: &completions, Parallelism: &parallelism}
	j := job(spec, v1alpha1.DenoJobStatus{})

	var runs []RunObservation
	res := reconcile(t, Observed{Job: j, Runs: runs})
	if len(res.CreateRuns) != 2 {
		t.Fatalf("first create = %v, want two runs", res.CreateRuns)
	}
	runs = apply(res, runs)

	started := 0
	for i := 0; i < 16 && !terminal(res.Phase); i++ {
		started += finishPending(runs)
		j.Status = v1alpha1.DenoJobStatus{
			Phase: res.Phase, Runs: res.RunNames, Succeeded: res.Succeeded,
			Failed: res.Failed, Active: res.Active, StartTime: res.StartTime,
		}
		res = reconcile(t, Observed{Job: j, Runs: runs})
		if res.Active > parallelism {
			t.Fatalf("active = %d, want at most %d", res.Active, parallelism)
		}
		runs = apply(res, runs)
	}
	if res.Phase != v1alpha1.DenoJobSucceeded {
		t.Fatalf("phase = %s, want Succeeded (runs=%v)", res.Phase, runs)
	}
	if res.Succeeded != completions {
		t.Fatalf("succeeded = %d, want %d", res.Succeeded, completions)
	}
	if started != int(completions) {
		t.Fatalf("started %d runs, want %d", started, completions)
	}
}

func TestBackoffExhaustionFailsTheJob(t *testing.T) {
	limit := int32(1)
	first := reconcile(t, Observed{
		Job:  job(v1alpha1.DenoJobSpec{BackoffLimit: &limit}, v1alpha1.DenoJobStatus{Phase: v1alpha1.DenoJobRunning}),
		Runs: []RunObservation{run("demo-1", v1alpha1.DenoRunFailed)},
	})
	if first.Phase != v1alpha1.DenoJobPending || !containsName(first.CreateRuns, "demo-2") {
		t.Fatalf("first: phase=%s create=%v", first.Phase, first.CreateRuns)
	}
	second := reconcile(t, Observed{
		Job: job(v1alpha1.DenoJobSpec{BackoffLimit: &limit}, v1alpha1.DenoJobStatus{
			Phase: v1alpha1.DenoJobRunning, Failed: first.Failed, Retries: first.Retries,
		}),
		Runs: []RunObservation{
			run("demo-1", v1alpha1.DenoRunFailed),
			run("demo-2", v1alpha1.DenoRunFailed),
		},
	})
	if second.Phase != v1alpha1.DenoJobFailed {
		t.Fatalf("second: phase=%s, want Failed", second.Phase)
	}
	if len(second.CreateRuns) != 0 {
		t.Fatalf("second creates runs after exhausting backoff: %v", second.CreateRuns)
	}
	if second.Failed != 2 || second.Retries != 2 {
		t.Fatalf("failed=%d retries=%d, want 2/2", second.Failed, second.Retries)
	}
}

func TestSuspendStopsActiveRuns(t *testing.T) {
	res := reconcile(t, Observed{
		Job: job(v1alpha1.DenoJobSpec{Suspend: true}, v1alpha1.DenoJobStatus{Phase: v1alpha1.DenoJobRunning}),
		Runs: []RunObservation{
			run("demo-1", v1alpha1.DenoRunRunning),
			run("demo-2", v1alpha1.DenoRunPending),
		},
	})
	if res.Phase != v1alpha1.DenoJobPending || res.Active != 0 {
		t.Fatalf("phase=%s active=%d", res.Phase, res.Active)
	}
	if len(res.StopRuns) != 2 {
		t.Fatalf("stop = %v, want both active runs", res.StopRuns)
	}
	if len(res.CreateRuns) != 0 {
		t.Fatalf("suspended job created runs: %v", res.CreateRuns)
	}
}

func TestDeadlineStopsRunsAndFails(t *testing.T) {
	deadline := int64(10)
	start := metav1.NewTime(base.Add(-20 * time.Second))
	res := reconcile(t, Observed{
		Job: job(v1alpha1.DenoJobSpec{ActiveDeadlineSeconds: &deadline}, v1alpha1.DenoJobStatus{
			Phase: v1alpha1.DenoJobRunning, StartTime: &start,
		}),
		Runs: []RunObservation{run("demo-1", v1alpha1.DenoRunRunning)},
	})
	if res.Phase != v1alpha1.DenoJobFailed {
		t.Fatalf("phase=%s, want Failed", res.Phase)
	}
	if !containsName(res.StopRuns, "demo-1") || res.CompletionTime == nil {
		t.Fatalf("stop=%v completion=%v", res.StopRuns, res.CompletionTime)
	}
}

func TestTTLDeletesRunsAndJob(t *testing.T) {
	ttl := int64(30)
	finished := metav1.NewTime(base.Add(-40 * time.Second))
	res := reconcile(t, Observed{
		Job: job(v1alpha1.DenoJobSpec{TTLSecondsAfterFinished: &ttl}, v1alpha1.DenoJobStatus{
			Phase: v1alpha1.DenoJobSucceeded, CompletionTime: &finished, RunName: "demo-1",
		}),
		Runs: []RunObservation{
			run("demo-1", v1alpha1.DenoRunSucceeded),
			run("demo-2", v1alpha1.DenoRunFailed),
		},
	})
	if !res.Delete || !res.DeleteJob {
		t.Fatalf("delete=%v deleteJob=%v", res.Delete, res.DeleteJob)
	}
	if !containsName(res.StopRuns, "demo-1") || !containsName(res.StopRuns, "demo-2") {
		t.Fatalf("stop=%v, want both children", res.StopRuns)
	}
}

func TestAnExtraCompletionDoesNotOverCount(t *testing.T) {
	completions := int32(5)
	var runs []RunObservation
	for i := 1; i <= 6; i++ {
		runs = append(runs, run("demo-"+strconv.Itoa(i), v1alpha1.DenoRunSucceeded))
	}
	res := reconcile(t, Observed{
		Job:  job(v1alpha1.DenoJobSpec{Completions: &completions}, v1alpha1.DenoJobStatus{Phase: v1alpha1.DenoJobRunning}),
		Runs: runs,
	})
	if res.Phase != v1alpha1.DenoJobSucceeded {
		t.Fatalf("phase=%s, want Succeeded", res.Phase)
	}
	if res.Succeeded != completions {
		t.Fatalf("succeeded=%d, want %d", res.Succeeded, completions)
	}
	if len(res.CreateRuns) != 0 {
		t.Fatalf("created runs after completion: %v", res.CreateRuns)
	}
}

func TestDeletionIsNotBlockedByAFinalizer(t *testing.T) {
	now := metav1.NewTime(base)
	obj := job(v1alpha1.DenoJobSpec{}, v1alpha1.DenoJobStatus{Phase: v1alpha1.DenoJobRunning, RunName: "demo-1"})
	obj.DeletionTimestamp = &now
	res := reconcile(t, Observed{Job: obj, Runs: []RunObservation{run("demo-1", v1alpha1.DenoRunRunning)}})
	if len(res.CreateRuns) != 0 || len(res.StopRuns) != 0 || res.Delete || res.DeleteJob {
		t.Fatalf("create=%v stop=%v delete=%v deleteJob=%v, want an owner-reference-driven teardown",
			res.CreateRuns, res.StopRuns, res.Delete, res.DeleteJob)
	}
}
