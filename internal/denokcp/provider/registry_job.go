package provider

import (
	"context"
	"encoding/json"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

func (r *Registry) ReadJob(ctx context.Context, ref Ref) (*v1alpha1.DenoJob, error) {
	return r.jobs.Get(ctx, ref)
}

func (r *Registry) CreateJob(ctx context.Context, logicalCluster string, job *v1alpha1.DenoJob) error {
	return r.jobs.Create(ctx, logicalCluster, job)
}

func (r *Registry) WriteJobStatus(ctx context.Context, ref Ref, st v1alpha1.DenoJobStatus) error {
	body, err := jobStatusPatch(st)
	if err != nil {
		return err
	}
	return r.jobs.PatchStatus(ctx, ref, body)
}

func (r *Registry) DeleteJob(ctx context.Context, ref Ref) error {
	return r.jobs.Delete(ctx, ref)
}

func jobStatusPatch(st v1alpha1.DenoJobStatus) ([]byte, error) {
	status := map[string]any{
		"phase":     string(st.Phase),
		"runName":   st.RunName,
		"active":    st.Active,
		"ready":     st.Ready,
		"succeeded": st.Succeeded,
		"failed":    st.Failed,
		"retries":   st.Retries,
		"exitCode":  st.ExitCode,
	}
	if st.Runs != nil {
		status["runs"] = st.Runs
	} else {
		status["runs"] = nil
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
