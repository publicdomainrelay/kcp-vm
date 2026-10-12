package provider

import (
	"context"
	"encoding/json"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

func (r *Registry) ListRuns(ctx context.Context, logicalCluster string) ([]v1alpha1.DenoRun, error) {
	return r.runs.List(ctx, logicalCluster)
}

func (r *Registry) ReadRun(ctx context.Context, ref Ref) (*v1alpha1.DenoRun, error) {
	return r.runs.Get(ctx, ref)
}

func (r *Registry) CreateRun(ctx context.Context, logicalCluster string, run *v1alpha1.DenoRun) error {
	return r.runs.Create(ctx, logicalCluster, run)
}

func (r *Registry) WriteRunStatus(ctx context.Context, ref Ref, st v1alpha1.DenoRunStatus) error {
	body, err := runStatusPatch(st)
	if err != nil {
		return err
	}
	return r.runs.PatchStatus(ctx, ref, body)
}

func (r *Registry) DeleteRun(ctx context.Context, ref Ref) error {
	return r.runs.Delete(ctx, ref)
}

func (r *Registry) RemoveRunFinalizer(ctx context.Context, ref Ref) error {
	return r.runs.RemoveFinalizer(ctx, ref, v1alpha1.FinalizerDenoRun)
}

func (r *Registry) RemoveRunFinalizerKnown(ctx context.Context, ref Ref, finalizers []string) error {
	return r.runs.RemoveKnownFinalizer(ctx, ref, finalizers, v1alpha1.FinalizerDenoRun)
}

func runStatusPatch(st v1alpha1.DenoRunStatus) ([]byte, error) {
	status := map[string]any{
		"phase":    string(st.Phase),
		"runID":    st.RunID,
		"message":  st.Message,
		"exitCode": st.ExitCode,
		"retries":  st.Retries,
	}
	if st.StartTime != nil {
		status["startTime"] = st.StartTime
	} else {
		status["startTime"] = nil
	}
	if st.CompletionTime != nil {
		status["completionTime"] = st.CompletionTime
	} else {
		status["completionTime"] = nil
	}
	if st.Outputs != nil {
		status["outputs"] = st.Outputs
	} else {
		status["outputs"] = nil
	}
	if st.Conditions != nil {
		status["conditions"] = st.Conditions
	} else {
		status["conditions"] = nil
	}
	return json.Marshal(map[string]any{"status": status})
}
