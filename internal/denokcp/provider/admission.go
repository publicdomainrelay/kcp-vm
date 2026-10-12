package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/queue"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/denocomputer"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/factory/admission"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/policyworkflowpod"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/policyworkflowrun"
)

type runAdmission struct {
	gated bool

	allowed bool

	reason string

	message string

	endpoint string

	workflow []byte

	inputs map[string]string

	ttl *int64

	preempt []Ref
}

func (a runAdmission) admission() *policyworkflowrun.Admission {
	if !a.gated {
		return nil
	}
	return &policyworkflowrun.Admission{Allowed: a.allowed, Reason: a.reason, Message: a.message}
}

const leaseTTL = 2 * time.Minute

func newRunAdmitter(p *Provider) *admission.Admitter {
	return admission.New(admission.Options{
		Source:    admissionSource{p: p},
		Leases:    queue.NewLeases(leaseTTL),
		LeaseTTL:  leaseTTL,
		Now:       p.opts.Now,
		Lifecycle: workflowRunLifecycle(),
	})
}

func workflowRunLifecycle() queue.Lifecycle {
	return queue.Lifecycle{
		Running:  denocomputer.RunningPhase,
		Terminal: denocomputer.TerminalPolicyWorkflow,
	}
}

type admissionSource struct {
	p *Provider
}

func (s admissionSource) Parent(ctx context.Context, run queue.Run) (Ref, bool, error) {
	obj, err := s.p.readPolicyRun(ctx, run.Ref)
	if err != nil {
		return Ref{}, false, err
	}
	if obj == nil {
		return Ref{}, false, nil
	}
	name := runPodName(obj)
	if name == "" {
		return Ref{}, false, nil
	}
	return Ref{LogicalCluster: run.Ref.LogicalCluster, Namespace: run.Ref.Namespace, Name: name}, true, nil
}

func (s admissionSource) Runs(ctx context.Context, parent Ref) ([]queue.Run, error) {
	runs, err := s.p.listWorkflowRunsForPod(ctx, parent.LogicalCluster, parent.Namespace, parent.Name)
	if err != nil {
		return nil, err
	}
	out := make([]queue.Run, 0, len(runs))
	for i := range runs {
		out = append(out, queue.Run{
			Ref:     Ref{LogicalCluster: parent.LogicalCluster, Namespace: runs[i].Namespace, Name: runs[i].Name},
			Phase:   string(runs[i].Status.Phase),
			Created: runs[i].CreationTimestamp.Time,
		})
	}
	return out, nil
}

func (s admissionSource) Capacity(ctx context.Context, parent Ref) (queue.Capacity, *queue.Blocker, error) {
	pod, err := s.p.readWorkflowPod(ctx, parent)
	if err != nil {
		return queue.Capacity{}, nil, err
	}
	if pod == nil {
		return queue.Capacity{}, &queue.Blocker{
			Reason:  denocomputer.ReasonPolicyWorkflowPodMissing,
			Message: fmt.Sprintf("the policy workflow pod %q does not exist", parent.Name),
		}, nil
	}
	if pod.Status.Endpoint == "" {
		return queue.Capacity{}, &queue.Blocker{
			Reason:  denocomputer.ReasonEngineNotReady,
			Message: "the referenced policy engine has no endpoint yet",
		}, nil
	}
	policy := v1alpha1.ConcurrencyForbid
	if pod.Spec.ConcurrencyPolicy != "" {
		policy = pod.Spec.ConcurrencyPolicy
	}
	return queue.Capacity{Policy: queue.Policy(policy), MaxConcurrent: pod.Spec.MaxConcurrent}, nil, nil
}

func runAdmissionFrom(adm queue.Admission, pod *v1alpha1.PolicyWorkflowPod, defaultTTL *int64) runAdmission {
	out := runAdmission{
		gated:   adm.Gated,
		allowed: adm.Allowed,
		reason:  adm.Reason,
		message: adm.Message,
		preempt: adm.Preempt,
		ttl:     policyworkflowpod.RunTTL(pod, defaultTTL),
	}
	if pod != nil {
		out.endpoint = pod.Status.Endpoint
		out.workflow = pod.Spec.Workflow.Raw
		out.inputs = pod.Spec.Inputs
	}
	return out
}

func (p *Provider) admitRun(ctx context.Context, ref Ref, run *v1alpha1.PolicyWorkflowRun) (queue.Admission, error) {
	return p.admissions.Admit(ctx, queue.Run{
		Ref:     ref.WithResourceVersion(""),
		Phase:   string(run.Status.Phase),
		Created: run.CreationTimestamp.Time,
	})
}

func (p *Provider) admit(ctx context.Context, ref Ref, run *v1alpha1.PolicyWorkflowRun) (runAdmission, error) {
	podName := runPodName(run)
	if podName == "" {
		return runAdmission{}, nil
	}
	adm, err := p.admitRun(ctx, ref, run)
	if err != nil {
		return runAdmission{}, err
	}
	pod, err := p.readWorkflowPod(ctx, Ref{LogicalCluster: ref.LogicalCluster, Namespace: ref.Namespace, Name: podName})
	if err != nil {
		return runAdmission{}, err
	}
	return runAdmissionFrom(adm, pod, p.opts.DefaultRunTTLSeconds), nil
}

func (p *Provider) wakeQueuedRuns(ctx context.Context, logicalCluster, namespace, podName string) {
	if podName == "" {
		return
	}
	pod, err := p.readWorkflowPod(ctx, Ref{LogicalCluster: logicalCluster, Namespace: namespace, Name: podName})
	if err != nil || pod == nil {
		return
	}
	runs, err := p.listWorkflowRunsForPod(ctx, logicalCluster, namespace, podName)
	if err != nil {
		return
	}
	qruns := make([]queue.Run, 0, len(runs))
	for i := range runs {
		qruns = append(qruns, queue.Run{
			Ref:     Ref{LogicalCluster: logicalCluster, Namespace: runs[i].Namespace, Name: runs[i].Name},
			Phase:   string(runs[i].Status.Phase),
			Created: runs[i].CreationTimestamp.Time,
		})
	}
	limit, unlimited := policyworkflowpod.Capacity(pod.Spec.ConcurrencyPolicy, pod.Spec.MaxConcurrent)
	for _, run := range queue.WakeList(qruns, workflowRunLifecycle(), limit, unlimited) {
		p.wake(workPolicyRun, run)
	}
	p.wake(workWorkflowPod, Ref{LogicalCluster: logicalCluster, Namespace: namespace, Name: podName})
}

func runPodName(run *v1alpha1.PolicyWorkflowRun) string {
	if run.Spec.PolicyWorkflowPod != "" {
		return run.Spec.PolicyWorkflowPod
	}
	return run.Labels[v1alpha1.PolicyWorkflowPodLabel]
}

func runningRunsFor(runs []v1alpha1.PolicyWorkflowRun, podName string) []string {
	var running []string
	for i := range runs {
		run := runs[i]
		if runPodName(&run) != podName {
			continue
		}
		if run.Status.Phase == v1alpha1.PolicyWorkflowRunning {
			running = append(running, run.Name)
		}
	}
	return running
}
