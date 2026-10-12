package provider

import (
	"context"
	"reflect"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/policyworkflowpod"
)

func (p *Provider) reconcileWorkflowPod(ctx context.Context, ref Ref, pod *v1alpha1.PolicyWorkflowPod) (time.Duration, bool, error) {
	ref.ResourceVersion = pod.ResourceVersion
	o := policyworkflowpod.Observed{Pod: *pod, Now: p.opts.Now()}
	if pod.Spec.PolicyEngine != "" {
		engine, err := p.readEngine(ctx, Ref{LogicalCluster: ref.LogicalCluster, Namespace: ref.Namespace, Name: pod.Spec.PolicyEngine})
		if err == nil && engine != nil {
			o.EngineReady = engine.Status.Ready && engine.Status.Endpoint != ""
			o.EngineEndpoint = engine.Status.Endpoint
		}
	}
	runs, err := p.listWorkflowRunsForPod(ctx, ref.LogicalCluster, ref.Namespace, pod.Name)
	if err != nil {
		return 0, false, err
	}
	o.Active = int32(len(runningRunsFor(runs, pod.Name)))
	res, err := p.opts.WorkflowPodDecider.Reconcile(ctx, o)
	if err != nil {
		return 0, false, err
	}
	if !p.opts.WriteStatus {
		return res.RequeueAfter, false, nil
	}
	status := v1alpha1.PolicyWorkflowPodStatus{
		Phase:      res.Phase,
		Endpoint:   res.Endpoint,
		Active:     res.Active,
		Conditions: res.Conditions,
	}
	if !reflect.DeepEqual(status, pod.Status) {
		if err := p.opts.Runtime.WriteWorkflowPodStatus(ctx, ref, status); err != nil {
			return 0, false, err
		}
	}
	return res.RequeueAfter, false, nil
}
