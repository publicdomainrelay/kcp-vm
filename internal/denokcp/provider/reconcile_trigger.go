package provider

import (
	"context"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/trigger"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/denocomputer"
)

func (p *Provider) reconcileTrigger(ctx context.Context, ref Ref, tr *v1alpha1.RunTrigger) (time.Duration, bool, error) {
	ref.ResourceVersion = tr.ResourceVersion
	o := trigger.Observed{Trigger: *tr, Now: p.opts.Now()}
	if tr.Spec.PolicyWorkflowPod != "" {
		if _, err := p.readWorkflowPod(ctx, Ref{LogicalCluster: ref.LogicalCluster, Namespace: ref.Namespace, Name: tr.Spec.PolicyWorkflowPod}); err != nil {
			return 0, false, err
		}
		runs, err := p.listWorkflowRuns(ctx, ref.LogicalCluster)
		if err != nil {
			return 0, false, err
		}
		latest, anyRun := latestTerminalRun(runs, tr.Spec.PolicyWorkflowPod)
		o.AnyRun = anyRun
		if latest != nil {
			o.Run = &trigger.RunObservation{
				Name:    latest.Name,
				Phase:   latest.Status.Phase,
				Outputs: latest.Status.Outputs,
			}
		}
	}
	res, err := p.opts.TriggerDecider.Reconcile(ctx, o)
	if err != nil {
		return 0, false, err
	}
	if !p.opts.WriteStatus {
		return triggerBackstop, false, nil
	}
	for _, op := range res.Ops {
		switch op {
		case trigger.OpCreateJob:
			if res.JobName != "" {
				job := newJobForTrigger(tr, res.JobName)
				if err := p.opts.Runtime.CreateJob(ctx, ref.LogicalCluster, job); err != nil && !apierrors.IsAlreadyExists(err) {
					return 0, false, err
				}
			}
		case trigger.OpRemoveFinalizer:
			if err := p.opts.Runtime.WriteTriggerStatus(ctx, ref, v1alpha1.RunTriggerStatus{
				Phase: res.Phase, Matched: res.Matched, JobName: res.JobName, LastRun: res.LastRun, Conditions: res.Conditions,
			}); err != nil {
				return 0, false, err
			}
		}
	}
	status := v1alpha1.RunTriggerStatus{
		Phase:      res.Phase,
		Matched:    res.Matched,
		JobName:    res.JobName,
		LastRun:    res.LastRun,
		Conditions: res.Conditions,
	}
	if !reflect.DeepEqual(status, tr.Status) {
		if err := p.opts.Runtime.WriteTriggerStatus(ctx, ref, status); err != nil {
			return 0, false, err
		}
	}
	return triggerBackstop, false, nil
}

func latestTerminalRun(runs []v1alpha1.PolicyWorkflowRun, podName string) (*v1alpha1.PolicyWorkflowRun, bool) {
	var latest *v1alpha1.PolicyWorkflowRun
	any := false
	for i := range runs {
		run := &runs[i]
		if run.Labels[v1alpha1.PolicyWorkflowPodLabel] != podName {
			continue
		}
		any = true
		if !terminalWorkflowPhase(run.Status.Phase) {
			continue
		}
		if latest == nil || run.CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = run
		}
	}
	return latest, any
}

func terminalWorkflowPhase(phase v1alpha1.PolicyWorkflowPhase) bool {
	return denocomputer.TerminalPolicyWorkflow(string(phase))
}
