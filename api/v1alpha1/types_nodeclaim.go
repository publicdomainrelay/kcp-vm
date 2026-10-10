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

	Version *int `json:"version,omitempty"`

	Phase ContractPhase `json:"phase,omitempty"`

	VMName string `json:"vmName,omitempty"`

	RequesterDid string `json:"requesterDid,omitempty"`

	MarketDid string `json:"marketDid,omitempty"`

	Payload *RecordRef `json:"payload,omitempty"`

	RFP *RecordRef `json:"rfp,omitempty"`

	Policy *RecordRef `json:"policy,omitempty"`

	Bid *RecordRef `json:"bid,omitempty"`

	Winner *ContractWinner `json:"winner,omitempty"`

	Accept *RecordRef `json:"accept,omitempty"`

	Receipt *RecordRef `json:"receipt,omitempty"`

	ReceiptOk *bool `json:"receiptOk,omitempty"`

	VMAddress string `json:"vmAddress,omitempty"`

	Bids *int `json:"bids,omitempty"`

	Release *ContractRelease `json:"release,omitempty"`

	Outcome ContractOutcome `json:"outcome,omitempty"`

	Error string `json:"error,omitempty"`

	Timestamps *ContractTimestamps `json:"timestamps,omitempty"`
}

type RecordRef struct {
	URI string `json:"uri"`

	CID string `json:"cid"`
}

type ContractPhase string

const (
	ContractRequested ContractPhase = "requested"

	ContractRFPSubmitted ContractPhase = "rfp_submitted"

	ContractWinnerSelected ContractPhase = "winner_selected"

	ContractAccepted ContractPhase = "accepted"

	ContractReceipt ContractPhase = "receipt"

	ContractNetwork ContractPhase = "network"

	ContractReleased ContractPhase = "released"
)

type ContractOutcome string

const (
	ContractOutcomeNoBids ContractOutcome = "no_bids"

	ContractOutcomeBidsDenied ContractOutcome = "bids_denied"

	ContractOutcomePolicyRejected ContractOutcome = "policy_rejected"

	ContractOutcomeReturned ContractOutcome = "returned"

	ContractOutcomeReceiptInvalid ContractOutcome = "receipt_invalid"

	ContractOutcomeNetworkTimeout ContractOutcome = "network_timeout"

	ContractOutcomeComplete ContractOutcome = "complete"
)

type ContractWinner struct {
	DID string `json:"did"`

	SubmitAccept string `json:"submitAccept,omitempty"`
}

type ContractRelease struct {
	Event RecordRef `json:"event"`

	Payload RecordRef `json:"payload"`

	OK bool `json:"ok"`

	Status *int `json:"status,omitempty"`
}

type ContractTimestamps struct {
	Requested string `json:"requested,omitempty"`

	RFPSubmitted string `json:"rfp_submitted,omitempty"`

	WinnerSelected string `json:"winner_selected,omitempty"`

	Accepted string `json:"accepted,omitempty"`

	Receipt string `json:"receipt,omitempty"`

	Network string `json:"network,omitempty"`

	Released string `json:"released,omitempty"`
}

type NodeClaimStatus struct {
	Phase NodeClaimPhase `json:"phase,omitempty"`

	Contract NodeClaimContract `json:"contract,omitempty"`

	Fqdn string `json:"fqdn,omitempty"`

	Message string `json:"message,omitempty"`
}

const FinalizerNodeClaim = "nodeclaim.socialweb.computer/release"
