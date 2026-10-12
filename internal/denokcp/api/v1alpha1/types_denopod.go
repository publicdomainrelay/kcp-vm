package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DenoPod struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DenoPodSpec   `json:"spec,omitempty"`
	Status DenoPodStatus `json:"status,omitempty"`
}

type DenoPodList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []DenoPod `json:"items"`
}

type DenoPodSpec struct {
	DenoPodTemplate `json:",inline"`

	RestartPolicy DenoRestartPolicy `json:"restartPolicy,omitempty"`

	ReadinessProbe *ExecProbe `json:"readinessProbe,omitempty"`

	LivenessProbe *ExecProbe `json:"livenessProbe,omitempty"`

	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	TTLSecondsAfterFinished *int64 `json:"ttlSecondsAfterFinished,omitempty"`
}

type DenoPodPhase string

const (
	DenoPodPending DenoPodPhase = "Pending"

	DenoPodRunning DenoPodPhase = "Running"

	DenoPodSucceeded DenoPodPhase = "Succeeded"

	DenoPodFailed DenoPodPhase = "Failed"
)

type DenoPodStatus struct {
	Phase DenoPodPhase `json:"phase,omitempty"`

	RunID string `json:"runID,omitempty"`

	Restarts int32 `json:"restarts,omitempty"`

	StartTime *metav1.Time `json:"startTime,omitempty"`

	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	ExitCode *int32 `json:"exitCode,omitempty"`

	Message string `json:"message,omitempty"`

	Outputs map[string]string `json:"outputs,omitempty"`

	Ready bool `json:"ready,omitempty"`

	Address string `json:"address,omitempty"`

	Listeners []DenoPodListener `json:"listeners,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type DenoPodListener struct {
	Name string `json:"name"`

	Kind string `json:"kind"`

	Address string `json:"address"`
}

const AnnotationEdge = "deno.computer/edge"

const ReasonFixedPortBehindTheEdge = "FixedPortBehindTheEdge"

const FinalizerDenoPod = "denopod.deno.computer/run"
