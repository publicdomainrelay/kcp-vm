package provider

import (
	"context"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/joballoc"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/denojob"
)

func (p *Provider) reconcileJob(ctx context.Context, ref Ref, job *v1alpha1.DenoJob, runs []v1alpha1.DenoRun) (time.Duration, bool, error) {
	ref.ResourceVersion = job.ResourceVersion
	observed := job.DeepCopy()
	key := jobAllocKey(ref)
	now := p.opts.Now()
	observed.Status.Runs = joballoc.MergeNames(observed.Status.Runs, p.jobAlloc.Names(key, now))
	observedRuns := jobRuns(runs, job.Name)
	if !jobTerminalPhase(observed.Status.Phase) {
		pending := p.jobAlloc.Pending(key, observedRunNames(observedRuns), now)
		for _, name := range pending {
			observedRuns = append(observedRuns, denojob.RunObservation{Name: name})
		}
	}
	o := denojob.Observed{Job: *observed, Runs: observedRuns, Now: now}
	res, err := p.opts.JobDecider.Reconcile(ctx, o)
	if err != nil {
		return 0, false, err
	}
	terminal := jobTerminalPhase(res.Phase)
	if !p.opts.WriteStatus {
		return res.RequeueAfter, terminal, nil
	}
	for _, name := range res.CreateRuns {
		if err := p.opts.Runtime.CreateRun(ctx, ref.LogicalCluster, newRunForJob(job, name)); err != nil {
			return 0, false, err
		}
		p.wake(workRun, Ref{LogicalCluster: ref.LogicalCluster, Namespace: ref.Namespace, Name: name})
	}
	if len(res.CreateRuns) > 0 {
		p.jobAlloc.Allocate(key, res.CreateRuns, now)
	}
	if terminal || res.DeleteJob {
		p.jobAlloc.Forget(key)
	}
	for _, name := range res.StopRuns {
		if err := p.opts.Runtime.DeleteRun(ctx, Ref{LogicalCluster: ref.LogicalCluster, Namespace: ref.Namespace, Name: name}); err != nil {
			return 0, false, err
		}
	}
	if res.DeleteJob {
		if err := p.opts.Runtime.DeleteJob(ctx, ref); err != nil {
			return 0, false, err
		}
	}
	status := v1alpha1.DenoJobStatus{
		Phase:          res.Phase,
		RunName:        res.RunName,
		Runs:           res.RunNames,
		StartTime:      res.StartTime,
		CompletionTime: res.CompletionTime,
		Active:         res.Active,
		Ready:          res.Ready,
		Succeeded:      res.Succeeded,
		Failed:         res.Failed,
		Retries:        res.Retries,
		ExitCode:       res.ExitCode,
		Outputs:        res.Outputs,
		Conditions:     res.Conditions,
	}
	if !reflect.DeepEqual(status, job.Status) {
		if p.jobWriteAllowed(ref, job, res, terminal) {
			if err := p.opts.Runtime.WriteJobStatus(ctx, ref, status); err != nil {
				return 0, false, err
			}
		} else if res.RequeueAfter <= 0 || res.RequeueAfter > jobStatusCooldown {
			res.RequeueAfter = jobStatusCooldown
		}
	}
	return res.RequeueAfter, terminal, nil
}

// ponytail: created run names live in memory until the next status write, so allocation does not force a write; phase, start and terminal writes stay immediate.
func (p *Provider) jobWriteAllowed(ref Ref, job *v1alpha1.DenoJob, res denojob.Result, terminal bool) bool {
	if terminal || res.Phase != job.Status.Phase ||
		(job.Status.StartTime == nil && res.StartTime != nil) {
		return true
	}
	key := ref.Key()
	p.jobWriteMu.Lock()
	defer p.jobWriteMu.Unlock()
	now := p.opts.Now()
	if last, ok := p.jobWriteAt[key]; ok && now.Sub(last) < jobStatusCooldown {
		return false
	}
	p.jobWriteAt[key] = now
	return true
}

// ponytail: the allocator key is the ref identity; job.ResourceVersion changes on every status write, so it is dropped the way the old ref.Key() did.
func jobAllocKey(ref Ref) Ref {
	ref.ResourceVersion = ""
	return ref
}

func observedRunNames(observed []denojob.RunObservation) []string {
	out := make([]string, 0, len(observed))
	for i := range observed {
		out = append(out, observed[i].Name)
	}
	return out
}

func jobRuns(runs []v1alpha1.DenoRun, jobName string) []denojob.RunObservation {
	var out []denojob.RunObservation
	for i := range runs {
		if runs[i].Labels[JobRunLabel] != jobName {
			continue
		}
		out = append(out, denojob.RunObservation{
			Name:     runs[i].Name,
			Phase:    runs[i].Status.Phase,
			ExitCode: runs[i].Status.ExitCode,
			Message:  runs[i].Status.Message,
			Outputs:  runs[i].Status.Outputs,
		})
	}
	return out
}

func newJobForTrigger(tr *v1alpha1.RunTrigger, name string) *v1alpha1.DenoJob {
	spec := v1alpha1.DenoJobSpec{}
	tr.Spec.JobTemplate.DeepCopyInto(&spec)
	return &v1alpha1.DenoJob{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "DenoJob"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       tr.Namespace,
			Labels:          map[string]string{"deno.computer/trigger": tr.Name},
			Annotations:     map[string]string{v1alpha1.AnnotationTTLAfterFinished: v1alpha1.DefaultFinishedTTL},
			OwnerReferences: []metav1.OwnerReference{ownerRef("RunTrigger", tr.Name, tr.UID)},
		},
		Spec: spec,
	}
}
