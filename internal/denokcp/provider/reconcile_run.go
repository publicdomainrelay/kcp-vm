package provider

import (
	"context"
	"fmt"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/denorun"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

func (p *Provider) reconcileRun(ctx context.Context, ref Ref, run *v1alpha1.DenoRun) (time.Duration, bool, error) {
	ref.ResourceVersion = run.ResourceVersion
	o := denorun.Observed{Run: *run, Now: p.opts.Now()}
	if run.Status.RunID != "" {
		st, err := p.opts.PodRunner.Observe(ctx, run.Status.RunID)
		if err != nil {
			return 0, false, fmt.Errorf("provider: observe deno run %s for %s: %w", run.Status.RunID, ref.Name, err)
		}
		o.Execution = &denorun.RunObservation{
			RunID:    run.Status.RunID,
			State:    denorun.RunState(st.State),
			ExitCode: exitCodePtr(st.ExitCode),
			Message:  st.Message,
			Outputs:  st.Outputs,
		}
	}
	res, err := p.opts.RunDecider.Reconcile(ctx, o)
	if err != nil {
		return 0, false, err
	}
	terminal := runTerminalPhase(res.Phase)
	if !p.opts.WriteStatus {
		return res.RequeueAfter, terminal, nil
	}
	runID := res.RunID
	startedRunID := ""
	started := false
	for _, op := range res.Ops {
		switch op {
		case denorun.OpStopRun:
			if run.Status.RunID != "" {
				if err := p.opts.PodRunner.Stop(ctx, run.Status.RunID); err != nil {
					return 0, false, err
				}
			}
		case denorun.OpStartRun:
			// ponytail: never start a second workload for a run this provider has already started. The cached object a pass reads can predate that pass's own start write, and denorun is a stateless decider whose only memory is the status it is handed, so a stale Pending copy makes it ask to start again. Measured on the drain harness at 1000 runs and parallelism 20: 1062 starts for 1000 runs, with 73 workloads live against a parallelism of 20, and none of it visible in wall clock. alreadyStarted keeps retries and recreated runs working.
			if p.alreadyStarted(ref, run) {
				return p.opts.MinTransitionPoll, false, nil
			}
			req, err := p.podRequest(ctx, ref, &run.Spec.DenoPodTemplate, "", projection{})
			if err != nil {
				return 0, false, err
			}
			id, err := p.opts.PodRunner.Start(ctx, req)
			if err != nil {
				return 0, false, fmt.Errorf("provider: start deno run %s: %w", ref.Name, err)
			}
			p.recordRunRef(ref, id, string(run.UID))
			p.noteRunActive()
			runID = id
			startedRunID = id
			started = true
		}
	}
	res = p.reobserveStartedRun(ctx, run, res, runID, started)
	switch {
	case res.RunIDCleared:
		runID = ""
	case res.RunID != "":
		runID = res.RunID
	}
	terminal = runTerminalPhase(res.Phase)
	status := v1alpha1.DenoRunStatus{
		RunID:          runID,
		StartTime:      res.StartTime,
		CompletionTime: res.CompletionTime,
		ExitCode:       res.ExitCode,
		Message:        res.Message,
		Outputs:        res.Outputs,
		Retries:        res.Retries,
		Conditions:     res.Conditions,
	}
	status.Phase = res.Phase
	if !reflect.DeepEqual(status, run.Status) {
		if err := p.opts.Runtime.WriteRunStatus(ctx, ref, status); err != nil {
			return 0, false, err
		}
	}
	for _, op := range res.Ops {
		switch op {
		case denorun.OpRemoveFinalizer:
			p.removeRunDirs(ctx, p.opts.PodRunner, "DenoRun", ref.Name, run.Status.RunID, runID, startedRunID)
			if err := p.removeRunFinalizer(ctx, ref, run.Finalizers); err != nil {
				return 0, false, err
			}
		case denorun.OpDelete:
			if err := p.opts.Runtime.DeleteRun(ctx, ref); err != nil {
				return 0, false, err
			}
		}
	}
	// ponytail: the record keeps the workload this pass started even when the decider dropped it from status.runID, because a pass that clears the id is exactly the pass whose own write a cached copy can predate. The TTL and the cached-terminal check still retire it, and a retry falls through alreadyStarted on its StartTime and Retries.
	keptRunID := runID
	if keptRunID == "" {
		keptRunID = startedRunID
	}
	p.keepRunRef(ref, keptRunID, string(run.UID), runTerminalPhase(run.Status.Phase))
	// ponytail: decrement only on the pass that actually transitions the run, because the same run reaches a terminal phase on several passes (the wake, the informer update, the backstop poll) and each of those would otherwise decrement the gauge again.
	if terminal && !runTerminalPhase(run.Status.Phase) {
		p.noteRunInactive()
	}
	if terminal {
		if jobName := run.Labels[JobRunLabel]; jobName != "" {
			p.wake(workJob, Ref{LogicalCluster: ref.LogicalCluster, Namespace: ref.Namespace, Name: jobName})
		}
	}
	return res.RequeueAfter, terminal, nil
}

// ponytail: a short run can finish before the first observation; re-observe once after start so its first and final status are one write. A running process keeps the usual two writes.
func (p *Provider) reobserveStartedRun(ctx context.Context, run *v1alpha1.DenoRun, res denorun.Result, runID string, started bool) denorun.Result {
	if !started || runID == "" {
		return res
	}
	st, err := p.opts.PodRunner.Observe(ctx, runID)
	if err != nil || st.State == runner.StateRunning {
		return res
	}
	primed := run.DeepCopy()
	primed.Status.RunID = runID
	primed.Status.Phase = v1alpha1.DenoRunRunning
	primed.Status.StartTime = res.StartTime
	primed.Status.Conditions = res.Conditions
	next, err := p.opts.RunDecider.Reconcile(ctx, denorun.Observed{
		Run: *primed,
		Execution: &denorun.RunObservation{
			RunID:    runID,
			State:    denorun.RunState(st.State),
			ExitCode: exitCodePtr(st.ExitCode),
			Message:  st.Message,
			Outputs:  st.Outputs,
		},
		Now: p.opts.Now(),
	})
	if err != nil {
		return res
	}
	return next
}

type runFinalizerRemover interface {
	RemoveRunFinalizerKnown(ctx context.Context, ref Ref, finalizers []string) error
}

// ponytail: the caller already holds the run's finalizers, so the patch does not need its own GET; a runtime without RemoveRunFinalizerKnown falls back to RemoveRunFinalizer.
func (p *Provider) removeRunFinalizer(ctx context.Context, ref Ref, finalizers []string) error {
	if remover, ok := p.opts.Runtime.(runFinalizerRemover); ok && len(finalizers) > 0 {
		return remover.RemoveRunFinalizerKnown(ctx, ref, finalizers)
	}
	return p.opts.Runtime.RemoveRunFinalizer(ctx, ref)
}

func newRunForJob(job *v1alpha1.DenoJob, name string) *v1alpha1.DenoRun {
	var spec v1alpha1.DenoRunSpec
	job.Spec.Template.DeepCopyInto(&spec)
	return &v1alpha1.DenoRun{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "DenoRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       job.Namespace,
			Finalizers:      []string{v1alpha1.FinalizerDenoRun},
			Labels:          map[string]string{JobRunLabel: job.Name},
			Annotations:     map[string]string{v1alpha1.AnnotationTTLAfterFinished: v1alpha1.DefaultFinishedTTL},
			OwnerReferences: []metav1.OwnerReference{ownerRef("DenoJob", job.Name, job.UID)},
		},
		Spec: spec,
	}
}
