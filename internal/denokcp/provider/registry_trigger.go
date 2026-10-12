package provider

import (
	"context"
	"encoding/json"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

func (r *Registry) ReadTrigger(ctx context.Context, ref Ref) (*v1alpha1.RunTrigger, error) {
	return r.triggers.Get(ctx, ref)
}

func (r *Registry) WriteTriggerStatus(ctx context.Context, ref Ref, st v1alpha1.RunTriggerStatus) error {
	body, err := triggerStatusPatch(st)
	if err != nil {
		return err
	}
	return r.triggers.PatchStatus(ctx, ref, body)
}

func triggerStatusPatch(st v1alpha1.RunTriggerStatus) ([]byte, error) {
	status := map[string]any{
		"phase":   string(st.Phase),
		"matched": st.Matched,
		"jobName": st.JobName,
		"lastRun": st.LastRun,
	}
	if st.Conditions != nil {
		status["conditions"] = st.Conditions
	} else {
		status["conditions"] = nil
	}
	return json.Marshal(map[string]any{"status": status})
}
