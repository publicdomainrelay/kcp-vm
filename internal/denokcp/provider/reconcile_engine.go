package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/policyengine"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

func (p *Provider) reconcileEngine(ctx context.Context, ref Ref, engine *v1alpha1.PolicyEngine) (time.Duration, bool, error) {
	ref.ResourceVersion = engine.ResourceVersion
	o := policyengine.Observed{Engine: *engine, Now: p.opts.Now(), Endpoint: engine.Status.Endpoint}
	if engine.Status.RunID != "" {
		st, err := p.opts.EngineRunner.Observe(ctx, engine.Status.RunID)
		if err != nil {
			return 0, false, fmt.Errorf("provider: observe engine run %s for %s: %w", engine.Status.RunID, ref.Name, err)
		}
		state := policyengine.ExecutionExited
		if st.State == runner.StateRunning {
			state = policyengine.ExecutionRunning
		}
		o.Execution = &policyengine.ExecutionObservation{RunID: engine.Status.RunID, State: state, Message: st.Message}
		if o.Endpoint == "" {
			o.Endpoint = engine.Status.Endpoint
		}
		p.observeEngineProbes(ctx, ref, engine, o.Endpoint, &o)
	}
	if o.Endpoint == "" {
		port, err := freePort()
		if err != nil {
			return 0, false, err
		}
		o.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	res, err := p.opts.EngineDecider.Reconcile(ctx, o)
	if err != nil {
		return 0, false, err
	}
	terminal := res.Phase == v1alpha1.PolicyEngineFailed
	if !p.opts.WriteStatus {
		return res.RequeueAfter, terminal, nil
	}
	runID := res.RunID
	for _, op := range res.Ops {
		switch op {
		case policyengine.OpStopEngine:
			if engine.Status.RunID != "" {
				if err := p.opts.EngineRunner.Stop(ctx, engine.Status.RunID); err != nil {
					return 0, false, err
				}
			}
		case policyengine.OpStartEngine:
			port, err := portFromEndpoint(res.Endpoint)
			if err != nil {
				return 0, false, err
			}
			id, err := p.opts.EngineRunner.Start(ctx, runner.EngineRequest{
				Name:           ref.Name,
				LogicalCluster: ref.LogicalCluster,
				Port:           port,
				Env:            p.engineEnv(engine.Spec.Env),
			})
			if err != nil {
				return 0, false, fmt.Errorf("provider: start engine %s: %w", ref.Name, err)
			}
			runID = id
		}
	}
	status := v1alpha1.PolicyEngineStatus{
		RunID:          runID,
		Endpoint:       res.Endpoint,
		Restarts:       res.Restarts,
		Ready:          res.Ready,
		StartTime:      res.StartTime,
		CompletionTime: res.CompletionTime,
		Message:        res.Message,
		Conditions:     res.Conditions,
	}
	status.Phase = res.Phase
	if !reflect.DeepEqual(status, engine.Status) {
		if err := p.opts.Runtime.WriteEngineStatus(ctx, ref, status); err != nil {
			return 0, false, err
		}
	}
	for _, op := range res.Ops {
		switch op {
		case policyengine.OpRemoveFinalizer:
			p.removeRunDirs(ctx, p.opts.EngineRunner, "PolicyEngine", ref.Name, engine.Status.RunID, runID)
			if err := p.opts.Runtime.RemoveEngineFinalizer(ctx, ref); err != nil {
				return 0, false, err
			}
		case policyengine.OpDelete:
			if err := p.opts.Runtime.DeleteEngine(ctx, ref); err != nil {
				return 0, false, err
			}
		}
	}
	return res.RequeueAfter, terminal, nil
}

func (p *Provider) observeEngineProbes(ctx context.Context, ref Ref, engine *v1alpha1.PolicyEngine, endpoint string, o *policyengine.Observed) {
	if engine.Spec.ReadinessProbe != nil && len(engine.Spec.ReadinessProbe.Command) > 0 {
		passed := p.runEngineProbe(ctx, engine.Status.RunID, engine.Spec.ReadinessProbe)
		o.ReadinessPassed = &passed
	} else if endpoint != "" {
		passed := httpOK(endpoint + "/health")
		o.ReadinessPassed = &passed
	}
	if engine.Spec.LivenessProbe != nil && len(engine.Spec.LivenessProbe.Command) > 0 {
		passed := p.runEngineProbe(ctx, engine.Status.RunID, engine.Spec.LivenessProbe)
		key := "engine/" + ref.Key()
		o.LivenessFailed = p.probes.Record(key, engine.Status.RunID, passed, thresholdOf(engine.Spec.LivenessProbe))
	}
}

func (p *Provider) runEngineProbe(ctx context.Context, runID string, probe *v1alpha1.ExecProbe) bool {
	timeout := DefaultProbeTimeout
	if probe.TimeoutSeconds != nil && *probe.TimeoutSeconds > 0 {
		timeout = time.Duration(*probe.TimeoutSeconds) * time.Second
	}
	passed, err := p.opts.EngineRunner.Probe(ctx, runID, probe.Command, timeout)
	if err != nil {
		return false
	}
	return passed
}

func (p *Provider) engineEnv(base map[string]string) map[string]string {
	if p.opts.BundledActionsDir == "" {
		return base
	}
	out := make(map[string]string, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["BUNDLED_ACTIONS_DIR"] = p.opts.BundledActionsDir
	return out
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("provider: pick a free port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func portFromEndpoint(endpoint string) (int, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, fmt.Errorf("provider: parse endpoint %q: %w", endpoint, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0, fmt.Errorf("provider: endpoint %q has no port: %w", endpoint, err)
	}
	return port, nil
}

func httpOK(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}
