package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ComputeContract struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ComputeContractSpec   `json:"spec,omitempty"`
	Status ComputeContractStatus `json:"status,omitempty"`
}

type ComputeContractList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ComputeContract `json:"items"`
}

type ComputeContractSpec struct {
	VMName string `json:"vmName,omitempty"`

	BidderDids []string `json:"bidderDids,omitempty"`

	DenyBidderDids []string `json:"denyBidderDids,omitempty"`

	SshAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`

	UserData string `json:"userData,omitempty"`

	Policy string `json:"policy,omitempty"`

	PolicyArgs string `json:"policyArgs,omitempty"`

	VMReadyTimeoutSec int `json:"vmReadyTimeoutSec,omitempty"`

	SecretsRef *VaultRef `json:"secretsRef,omitempty"`

	Release bool `json:"release,omitempty"`
}

type ComputeContractPhase string

const (
	ComputeContractPending ComputeContractPhase = "Pending"

	ComputeContractRequesting ComputeContractPhase = "Requesting"

	ComputeContractHeld ComputeContractPhase = "Held"

	ComputeContractReleasing ComputeContractPhase = "Releasing"

	ComputeContractReleased ComputeContractPhase = "Released"

	ComputeContractFailed ComputeContractPhase = "Failed"
)

type ComputeContractStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase ComputeContractPhase `json:"phase,omitempty"`

	Message string `json:"message,omitempty"`

	SecretsUrl string `json:"secretsUrl,omitempty"`

	SecretsFetchedAt string `json:"secretsFetchedAt,omitempty"`

	Contract *NodeClaimContract `json:"contract,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`

	Execution *ExecutionStatus `json:"execution,omitempty"`
}
