package provider

import (
	"context"
	"encoding/json"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

func (r *Registry) ReadEngine(ctx context.Context, ref Ref) (*v1alpha1.PolicyEngine, error) {
	return r.engines.Get(ctx, ref)
}

func (r *Registry) WriteEngineStatus(ctx context.Context, ref Ref, st v1alpha1.PolicyEngineStatus) error {
	body, err := engineStatusPatch(st)
	if err != nil {
		return err
	}
	return r.engines.PatchStatus(ctx, ref, body)
}

func (r *Registry) DeleteEngine(ctx context.Context, ref Ref) error {
	return r.engines.Delete(ctx, ref)
}

func (r *Registry) RemoveEngineFinalizer(ctx context.Context, ref Ref) error {
	return r.engines.RemoveFinalizer(ctx, ref, v1alpha1.FinalizerPolicyEngine)
}

func engineStatusPatch(st v1alpha1.PolicyEngineStatus) ([]byte, error) {
	status := map[string]any{
		"phase":    string(st.Phase),
		"runID":    st.RunID,
		"endpoint": st.Endpoint,
		"restarts": st.Restarts,
		"ready":    st.Ready,
		"message":  st.Message,
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
	if st.Conditions != nil {
		status["conditions"] = st.Conditions
	} else {
		status["conditions"] = nil
	}
	return json.Marshal(map[string]any{"status": status})
}
