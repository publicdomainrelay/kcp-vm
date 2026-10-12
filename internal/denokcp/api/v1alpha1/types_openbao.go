package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type OpenBao struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoSpec   `json:"spec,omitempty"`
	Status OpenBaoStatus `json:"status,omitempty"`
}

type OpenBaoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []OpenBao `json:"items"`
}

type OpenBaoSpec struct {
	Namespace string `json:"namespace"`
}

type OpenBaoStatus struct {
	Namespace string `json:"namespace,omitempty"`

	Serial string `json:"serial,omitempty"`

	Chain string `json:"chain,omitempty"`

	Ready bool `json:"ready,omitempty"`

	Message string `json:"message,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const (
	FinalizerOpenBao = "openbao.deno.computer/namespace"

	OpenBaoConditionReady = "Ready"

	OpenBaoConditionAmbiguous = "Ambiguous"
)
