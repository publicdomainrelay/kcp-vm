package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DenoRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DenoRunSpec   `json:"spec,omitempty"`
	Status DenoRunStatus `json:"status,omitempty"`
}

type DenoRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []DenoRun `json:"items"`
}

type DenoRunSpec struct {
	DenoPodTemplate `json:",inline"`

	Backoff *int32 `json:"backoff,omitempty"`

	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	TTLSecondsAfterFinished *int64 `json:"ttlSecondsAfterFinished,omitempty"`
}

type DenoRunPhase string

const (
	DenoRunPending DenoRunPhase = "Pending"

	DenoRunRunning DenoRunPhase = "Running"

	DenoRunSucceeded DenoRunPhase = "Succeeded"

	DenoRunFailed DenoRunPhase = "Failed"
)

type DenoRunStatus struct {
	Phase DenoRunPhase `json:"phase,omitempty"`

	RunID string `json:"runID,omitempty"`

	StartTime *metav1.Time `json:"startTime,omitempty"`

	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	ExitCode *int32 `json:"exitCode,omitempty"`

	Message string `json:"message,omitempty"`

	Outputs map[string]string `json:"outputs,omitempty"`

	Retries int32 `json:"retries,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const FinalizerDenoRun = "denorun.deno.computer/run"
