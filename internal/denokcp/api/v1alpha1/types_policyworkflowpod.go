package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type PolicyWorkflowPod struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolicyWorkflowPodSpec   `json:"spec,omitempty"`
	Status PolicyWorkflowPodStatus `json:"status,omitempty"`
}

type PolicyWorkflowPodList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []PolicyWorkflowPod `json:"items"`
}

type PolicyWorkflowPodSpec struct {
	Workflow runtime.RawExtension `json:"workflow"`

	Inputs map[string]string `json:"inputs,omitempty"`

	Args map[string]string `json:"args,omitempty"`

	Perspective string `json:"perspective,omitempty"`

	SelfDid string `json:"selfDid,omitempty"`

	PolicyEngine string `json:"policyEngine"`

	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	MaxConcurrent *int32 `json:"maxConcurrent,omitempty"`

	RunTTLSecondsAfterFinished *int64 `json:"runTTLSecondsAfterFinished,omitempty"`
}

type PolicyWorkflowPodPhase string

const (
	PolicyWorkflowPodPending PolicyWorkflowPodPhase = "Pending"

	PolicyWorkflowPodRunning PolicyWorkflowPodPhase = "Running"

	PolicyWorkflowPodFailed PolicyWorkflowPodPhase = "Failed"
)

type PolicyWorkflowPodStatus struct {
	Phase PolicyWorkflowPodPhase `json:"phase,omitempty"`

	Endpoint string `json:"endpoint,omitempty"`

	Active int32 `json:"active,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const PolicyWorkflowPodLabel = "deno.computer/policyworkflowpod"
