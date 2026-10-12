package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RunTrigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunTriggerSpec   `json:"spec,omitempty"`
	Status RunTriggerStatus `json:"status,omitempty"`
}

type RunTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []RunTrigger `json:"items"`
}

type RunTriggerSpec struct {
	PolicyWorkflowPod string `json:"policyWorkflowPod"`

	Match map[string]string `json:"match,omitempty"`

	JobTemplate DenoJobSpec `json:"jobTemplate"`
}

type RunTriggerPhase string

const (
	RunTriggerPending RunTriggerPhase = "Pending"

	RunTriggerTriggered RunTriggerPhase = "Triggered"

	RunTriggerSkipped RunTriggerPhase = "Skipped"

	RunTriggerFailed RunTriggerPhase = "Failed"
)

type RunTriggerStatus struct {
	Phase RunTriggerPhase `json:"phase,omitempty"`

	Matched bool `json:"matched,omitempty"`

	JobName string `json:"jobName,omitempty"`

	LastRun string `json:"lastRun,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
