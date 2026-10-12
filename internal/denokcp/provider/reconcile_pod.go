package provider

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/denospec"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/factory/servicenames"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/denopod"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

func (p *Provider) reconcilePod(ctx context.Context, ref Ref, pod *v1alpha1.DenoPod) (time.Duration, bool, error) {
	ref.ResourceVersion = pod.ResourceVersion
	o := denopod.Observed{Pod: *pod, Now: p.opts.Now()}
	address := ""
	if pod.Status.RunID != "" {
		st, err := p.opts.PodRunner.Observe(ctx, pod.Status.RunID)
		if err != nil {
			return 0, false, fmt.Errorf("provider: observe pod run %s for %s: %w", pod.Status.RunID, ref.Name, err)
		}
		state := denopod.ExecutionExited
		if st.State == runner.StateRunning {
			state = denopod.ExecutionRunning
			address = servicenames.BoundAddress(pod.Spec.Env["SERVICE_ARGS"], pod.Spec.Env["SERVICE_ENV"], st.BoundPort)
		}
		if address != pod.Status.Address {
			p.publishAddress(ctx, ref, address)
		}
		o.Execution = &denopod.ExecutionObservation{
			RunID:    pod.Status.RunID,
			State:    state,
			ExitCode: exitCodePtr(st.ExitCode),
			Message:  st.Message,
			Outputs:  st.Outputs,
		}
		p.observeProbes(ctx, ref, pod, &o)
	}
	res, err := p.opts.PodDecider.Reconcile(ctx, o)
	if err != nil {
		return 0, false, err
	}
	terminal := podTerminalPhase(res.Phase)
	if !p.opts.WriteStatus {
		return res.RequeueAfter, terminal, nil
	}
	runID := res.RunID
	if runID != pod.Status.RunID || terminal {
		if address != "" || pod.Status.Address != "" {
			p.publishAddress(ctx, ref, "")
		}
		address = ""
	}
	for _, op := range res.Ops {
		switch op {
		case denopod.OpStopRun:
			if pod.Status.RunID != "" {
				if err := p.opts.PodRunner.Stop(ctx, pod.Status.RunID); err != nil {
					return 0, false, err
				}
			}
		case denopod.OpStartRun:
			req, err := p.podRequest(ctx, ref, &pod.Spec.DenoPodTemplate)
			if err != nil {
				return 0, false, err
			}
			id, err := p.opts.PodRunner.Start(ctx, req)
			if err != nil {
				return 0, false, fmt.Errorf("provider: start pod %s: %w", ref.Name, err)
			}
			runID = id
		}
	}
	status := v1alpha1.DenoPodStatus{
		RunID:          runID,
		Restarts:       res.Restarts,
		StartTime:      res.StartTime,
		CompletionTime: res.CompletionTime,
		ExitCode:       res.ExitCode,
		Message:        res.Message,
		Outputs:        res.Outputs,
		Ready:          res.Ready,
		Address:        address,
		Conditions:     res.Conditions,
	}
	status.Phase = res.Phase
	if !reflect.DeepEqual(status, pod.Status) {
		if err := p.opts.Runtime.WritePodStatus(ctx, ref, status); err != nil {
			return 0, false, err
		}
	}
	if _, err := p.serviceResolver().WriteTableFile(ctx); err != nil {
		p.opts.Log.Warn("kcpdns: the table file was not rewritten, peers keep the addresses they had", "err", err)
	}
	for _, op := range res.Ops {
		switch op {
		case denopod.OpRemoveFinalizer:
			if err := p.opts.Runtime.RemovePodFinalizer(ctx, ref); err != nil {
				return 0, false, err
			}
		case denopod.OpDelete:
			if err := p.opts.Runtime.DeletePod(ctx, ref); err != nil {
				return 0, false, err
			}
		}
	}
	return res.RequeueAfter, terminal, nil
}

func (p *Provider) observeProbes(ctx context.Context, ref Ref, pod *v1alpha1.DenoPod, o *denopod.Observed) {
	if pod.Spec.ReadinessProbe != nil && len(pod.Spec.ReadinessProbe.Command) > 0 {
		passed := p.runProbe(ctx, pod.Status.RunID, pod.Spec.ReadinessProbe)
		o.ReadinessPassed = &passed
	}
	if pod.Spec.LivenessProbe != nil && len(pod.Spec.LivenessProbe.Command) > 0 {
		passed := p.runProbe(ctx, pod.Status.RunID, pod.Spec.LivenessProbe)
		key := ref.Key()
		o.LivenessFailed = p.probes.Record(key, pod.Status.RunID, passed, thresholdOf(pod.Spec.LivenessProbe))
	}
}

func (p *Provider) runProbe(ctx context.Context, runID string, probe *v1alpha1.ExecProbe) bool {
	timeout := DefaultProbeTimeout
	if probe.TimeoutSeconds != nil && *probe.TimeoutSeconds > 0 {
		timeout = time.Duration(*probe.TimeoutSeconds) * time.Second
	}
	// ponytail: a shim probe that cannot be expanded is skipped rather than run,
	// because PodRunner.Probe treats an empty command as a pass; passing nil
	// through would mark the workload Ready on the strength of having no probe.
	command := p.probeCommand(probe.Command)
	if command == nil {
		p.opts.Log.Warn("kcpdns: a probe names a service the shim cannot resolve, skipping it", "runID", runID)
		return false
	}
	passed, err := p.opts.PodRunner.Probe(ctx, runID, command, timeout)
	if err != nil {
		return false
	}
	return passed
}

func (p *Provider) podRequest(ctx context.Context, ref Ref, tmpl *v1alpha1.DenoPodTemplate) (runner.PodRequest, error) {
	args, err := denospec.Args(tmpl.Permissions.DenoSpec())
	if err != nil {
		return runner.PodRequest{}, fmt.Errorf("provider: %s permissions: %w", ref.Name, err)
	}
	req := runner.PodRequest{
		Name:           ref.Name,
		LogicalCluster: ref.LogicalCluster,
		DenoJSON:       string(tmpl.DenoJSON.Raw),
		DenoLock:       tmpl.DenoLock,
		Script:         tmpl.Script,
		PermissionArgs: args,
		Env:            p.podEnv(ctx, ref, tmpl),
		Server:         p.serverFor(tmpl, ref),
		Workspace:      ref.LogicalCluster,
	}
	if tmpl.ServiceAccount != nil {
		if p.opts.Minter == nil {
			return runner.PodRequest{}, fmt.Errorf("provider: %s names a service account but the provider has no token minter", ref.Name)
		}
		token, err := p.opts.Minter.MintServiceAccountToken(ctx, ref.LogicalCluster,
			tmpl.ServiceAccount.Namespace, tmpl.ServiceAccount.Name, p.opts.TokenTTL)
		if err != nil {
			return runner.PodRequest{}, err
		}
		req.Token = token
	}
	return req, nil
}

func (p *Provider) serverFor(tmpl *v1alpha1.DenoPodTemplate, target Ref) string {
	if tmpl.APIServer != "" {
		return tmpl.APIServer
	}
	if p.opts.Host == "" {
		return ""
	}
	return ref.BaseHost(p.opts.Host) + "/clusters/" + target.LogicalCluster
}
