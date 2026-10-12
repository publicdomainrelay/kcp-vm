package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type PolicyWorkflowRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolicyWorkflowRunSpec   `json:"spec,omitempty"`
	Status PolicyWorkflowRunStatus `json:"status,omitempty"`
}

type PolicyWorkflowRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []PolicyWorkflowRun `json:"items"`
}

type PolicyWorkflowRunSpec struct {
	Workflow runtime.RawExtension `json:"workflow"`

	Inputs map[string]string `json:"inputs,omitempty"`

	Args map[string]string `json:"args,omitempty"`

	Perspective string `json:"perspective,omitempty"`

	SelfDid string `json:"selfDid,omitempty"`

	PolicyWorkflowPod string `json:"policyWorkflowPod,omitempty"`

	EngineEndpoint string `json:"engineEndpoint,omitempty"`

	Suspend bool `json:"suspend,omitempty"`

	Cancel bool `json:"cancel,omitempty"`

	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	TTLSecondsAfterFinished *int64 `json:"ttlSecondsAfterFinished,omitempty"`
}

type PolicyWorkflowPhase string

const (
	PolicyWorkflowPending PolicyWorkflowPhase = "Pending"

	PolicyWorkflowRunning PolicyWorkflowPhase = "Running"

	PolicyWorkflowSucceeded PolicyWorkflowPhase = "Succeeded"

	PolicyWorkflowFailed PolicyWorkflowPhase = "Failed"

	PolicyWorkflowCancelled PolicyWorkflowPhase = "Cancelled"
)

type PolicyWorkflowRunStatus struct {
	Phase PolicyWorkflowPhase `json:"phase,omitempty"`

	RunID string `json:"runID,omitempty"`

	StartTime *metav1.Time `json:"startTime,omitempty"`

	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	Active int32 `json:"active,omitempty"`

	Succeeded int32 `json:"succeeded,omitempty"`

	Failed int32 `json:"failed,omitempty"`

	Retries int32 `json:"retries,omitempty"`

	ExitStatus string `json:"exitStatus,omitempty"`

	Outputs map[string]string `json:"outputs,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const FinalizerPolicyWorkflowRun = "policyworkflowrun.deno.computer/run"
