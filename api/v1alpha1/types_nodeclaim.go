package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NodeClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeClaimSpec   `json:"spec,omitempty"`
	Status NodeClaimStatus `json:"status,omitempty"`
}

type NodeClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []NodeClaim `json:"items"`
}

type NodeClaimSpec struct {
	Cluster string `json:"cluster"`

	NodeName string `json:"nodeName"`

	OwnerDid string `json:"ownerDid"`

	ApiserverPort int `json:"apiserverPort"`

	AdvertiseAddress string `json:"advertiseAddress"`

	TokenFile string `json:"tokenFile"`

	NodePasswordFile string `json:"nodePasswordFile"`

	ServerCAFile string `json:"serverCAFile"`

	DenyBidderDids []string `json:"denyBidderDids,omitempty"`
}

type NodeClaimPhase string

const (
	NodeClaimPending NodeClaimPhase = "Pending"

	NodeClaimBidding NodeClaimPhase = "Bidding"

	NodeClaimProvisioning NodeClaimPhase = "Provisioning"

	NodeClaimJoining NodeClaimPhase = "Joining"

	NodeClaimRunning NodeClaimPhase = "Running"

	NodeClaimFailed NodeClaimPhase = "Failed"

	NodeClaimReleased NodeClaimPhase = "Released"
)

type NodeClaimContract struct {
	VMUri string `json:"vmUri,omitempty"`

	RFPUri string `json:"rfpUri,omitempty"`

	AcceptUri string `json:"acceptUri,omitempty"`

	ReceiptUri string `json:"receiptUri,omitempty"`

	ReceiptCid string `json:"receiptCid,omitempty"`

	SubmitEventRef string `json:"submitEventRef,omitempty"`
}

type NodeClaimStatus struct {
	Phase NodeClaimPhase `json:"phase,omitempty"`

	Contract NodeClaimContract `json:"contract,omitempty"`

	Fqdn string `json:"fqdn,omitempty"`

	Message string `json:"message,omitempty"`
}

const FinalizerNodeClaim = "nodeclaim.socialweb.computer/release"
