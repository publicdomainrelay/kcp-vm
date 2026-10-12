package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DenoJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DenoJobSpec   `json:"spec,omitempty"`
	Status DenoJobStatus `json:"status,omitempty"`
}

type DenoJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []DenoJob `json:"items"`
}

type DenoJobSpec struct {
	Template DenoRunSpec `json:"template"`

	Completions *int32 `json:"completions,omitempty"`

	Parallelism *int32 `json:"parallelism,omitempty"`

	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	Suspend bool `json:"suspend,omitempty"`

	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	TTLSecondsAfterFinished *int64 `json:"ttlSecondsAfterFinished,omitempty"`
}

type DenoJobPhase string

const (
	DenoJobPending DenoJobPhase = "Pending"

	DenoJobRunning DenoJobPhase = "Running"

	DenoJobSucceeded DenoJobPhase = "Succeeded"

	DenoJobFailed DenoJobPhase = "Failed"
)

type DenoJobStatus struct {
	Phase DenoJobPhase `json:"phase,omitempty"`

	RunName string `json:"runName,omitempty"`

	Runs []string `json:"runs,omitempty"`

	StartTime *metav1.Time `json:"startTime,omitempty"`

	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	Active int32 `json:"active,omitempty"`

	Ready int32 `json:"ready,omitempty"`

	Succeeded int32 `json:"succeeded,omitempty"`

	Failed int32 `json:"failed,omitempty"`

	Retries int32 `json:"retries,omitempty"`

	ExitCode *int32 `json:"exitCode,omitempty"`

	Outputs map[string]string `json:"outputs,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
