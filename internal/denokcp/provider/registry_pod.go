package provider

import (
	"context"
	"encoding/json"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

func (r *Registry) ReadPod(ctx context.Context, ref Ref) (*v1alpha1.DenoPod, error) {
	return r.pods.Get(ctx, ref)
}

func (r *Registry) CreatePod(ctx context.Context, logicalCluster string, pod *v1alpha1.DenoPod) error {
	return r.pods.Create(ctx, logicalCluster, pod)
}

func (r *Registry) WritePodStatus(ctx context.Context, ref Ref, st v1alpha1.DenoPodStatus) error {
	body, err := podStatusPatch(st)
	if err != nil {
		return err
	}
	return r.pods.PatchStatus(ctx, ref, body)
}

func (r *Registry) DeletePod(ctx context.Context, ref Ref) error {
	return r.pods.Delete(ctx, ref)
}

func (r *Registry) RemovePodFinalizer(ctx context.Context, ref Ref) error {
	return r.pods.RemoveFinalizer(ctx, ref, v1alpha1.FinalizerDenoPod)
}

func podStatusPatch(st v1alpha1.DenoPodStatus) ([]byte, error) {
	status := map[string]any{
		"phase":    string(st.Phase),
		"runID":    st.RunID,
		"restarts": st.Restarts,
		"message":  st.Message,
		"exitCode": st.ExitCode,
		"ready":    st.Ready,
		"address":  st.Address,
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
