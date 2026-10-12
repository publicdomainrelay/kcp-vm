package provider

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/memoryrunner"
)

func runGuardFixture(t *testing.T, pollsBeforeDone int) (*fakeRuntime, Ref, *Provider, *memoryrunner.Pod) {
	t.Helper()
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "run-1"}
	rt.runs[ref] = &v1alpha1.DenoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Generation: 1, UID: "uid-1"},
		Spec:       v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}},
	}
	mem := memoryrunner.NewPod(memoryrunner.PodOptions{
		Outcome:         runner.PodStatus{State: runner.StateSucceeded, Outputs: map[string]string{"allow": "true"}},
		PollsBeforeDone: pollsBeforeDone,
	})
	p := runtimeProvider(t, rt, mem, nil)
	return rt, ref, p, mem
}

// ponytail: the regression test for the duplicate-start bug. Before the guard, the drain harness started 1062 workloads for 1000 runs. A cached copy that predates a pass's own start write is exactly the shape a pass reads under load, and it makes a stateless decider ask to start again.
func TestAStaleCachedRunDoesNotStartASecondWorkload(t *testing.T) {
	rt, ref, p, _ := runGuardFixture(t, 2)
	ctx := context.Background()
	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	started := rt.runs[ref].Status.RunID
	if started == "" {
		t.Fatal("the run should have started a deno process")
	}
	if got := p.RunStarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}

	rt.runs[ref].Status = v1alpha1.DenoRunStatus{}
	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := p.RunStarts(); got != 1 {
		t.Fatalf("starts = %d, want 1: a cached copy predating the start write must not start a second workload", got)
	}

	rt.runs[ref].Status = v1alpha1.DenoRunStatus{RunID: started, Phase: v1alpha1.DenoRunRunning}
	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := rt.runs[ref].Status.RunID; got != started {
		t.Fatalf("run ended on %s, want the original workload %s", got, started)
	}
	if got := rt.runs[ref].Status.Phase; got != v1alpha1.DenoRunSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", got)
	}
}

func TestARunStartedAndFinishedInOnePassLeavesNothingActive(t *testing.T) {
	_, ref, p, _ := runGuardFixture(t, 1)
	ctx := context.Background()
	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := p.RunStarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	if got := p.ActiveRuns(); got != 0 {
		t.Fatalf("active runs = %d after the run finished, want 0", got)
	}
}

// ponytail: a run that fails must still be able to start a fresh workload, so this pins that the start guard does not swallow a retry. The pass that fails a run clears status.runID, so every pass that reaches the guard here is a retry that carries Retries and StartTime in its cached status.
func TestAFailingRunRetries(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "run-1"}
	backoff := int32(2)
	rt.runs[ref] = &v1alpha1.DenoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Generation: 1, UID: "uid-1"},
		Spec: v1alpha1.DenoRunSpec{
			DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "exit 1"},
			Backoff:         &backoff,
		},
	}
	mem := memoryrunner.NewPod(memoryrunner.PodOptions{
		Outcome:         runner.PodStatus{State: runner.StateFailed, ExitCode: 1, Message: "boom"},
		PollsBeforeDone: 1,
	})
	p := runtimeProvider(t, rt, mem, nil)
	ctx := context.Background()

	for i := 1; i <= 4; i++ {
		if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		record, indexed := p.runRefs.Lookup(ref)
		t.Logf("pass %d: starts=%d phase=%s retries=%d runID=%q known=%q",
			i, p.RunStarts(), rt.runs[ref].Status.Phase, rt.runs[ref].Status.Retries, rt.runs[ref].Status.RunID, record.RunID)
		if !runTerminalPhase(rt.runs[ref].Status.Phase) && (!indexed || record.RunID == "") {
			t.Fatalf("pass %d: a run whose workload this provider started is missing from the runRefs index, so the start guard cannot stand a stale cached copy down", i)
		}
	}
	if got := rt.runs[ref].Status.Phase; got != v1alpha1.DenoRunFailed {
		t.Fatalf("phase = %s, want Failed once the backoff is exceeded", got)
	}
	if got := p.RunStarts(); got != 3 {
		t.Fatalf("starts = %d, want 3: a backoff of 2 is the initial attempt plus two retries", got)
	}
	if got := rt.runs[ref].Status.Retries; got != 3 {
		t.Fatalf("retries = %d, want 3: one per attempt, so a retry is never spent re-observing a workload that already failed", got)
	}
	if got := rt.runs[ref].Status.RunID; got != "" {
		t.Fatalf("runID = %q, want it cleared: the workload it names is dead", got)
	}
}

// ponytail: a run recreated behind the same name carries a new UID, and the guard must not mistake it for the run it replaced.
func TestARecreatedRunBehindTheSameNameStarts(t *testing.T) {
	rt, ref, p, _ := runGuardFixture(t, 2)
	ctx := context.Background()
	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := p.RunStarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}

	rt.runs[ref] = &v1alpha1.DenoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Generation: 1, UID: "uid-2"},
		Spec:       v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}},
	}
	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := p.RunStarts(); got != 2 {
		t.Fatalf("starts = %d, want 2: a recreated run is a new object and must start its own workload", got)
	}
}
