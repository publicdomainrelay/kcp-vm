package provider

import (
	"context"
	"encoding/json"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

func (r *Registry) ReadWorkflowPod(ctx context.Context, ref Ref) (*v1alpha1.PolicyWorkflowPod, error) {
	return r.workflowPods.Get(ctx, ref)
}

func (r *Registry) WriteWorkflowPodStatus(ctx context.Context, ref Ref, st v1alpha1.PolicyWorkflowPodStatus) error {
	body, err := workflowPodStatusPatch(st)
	if err != nil {
		return err
	}
	return r.workflowPods.PatchStatus(ctx, ref, body)
}

func (r *Registry) DeleteWorkflowPod(ctx context.Context, ref Ref) error {
	return r.workflowPods.Delete(ctx, ref)
}

func workflowPodStatusPatch(st v1alpha1.PolicyWorkflowPodStatus) ([]byte, error) {
	status := map[string]any{
		"phase":    string(st.Phase),
		"endpoint": st.Endpoint,
		"active":   st.Active,
	}
	if st.Conditions != nil {
		status["conditions"] = st.Conditions
	} else {
		status["conditions"] = nil
	}
	return json.Marshal(map[string]any{"status": status})
}
