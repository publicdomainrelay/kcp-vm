package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MarketNetworkValue struct {
	PlcDirectoryUrl string `json:"plcDirectoryUrl"`

	IngressProxyHost string `json:"ingressProxyHost"`

	RelayUrls []string `json:"relayUrls"`

	FirehoseUrls []string `json:"firehoseUrls"`

	QrOrigin string `json:"qrOrigin"`

	GuestTlsUrl string `json:"guestTlsUrl,omitempty"`

	CaBundleRef string `json:"caBundleRef,omitempty"`
}

type ApplicationKind string

const (
	ApplicationKindMarketBidder ApplicationKind = "MarketBidder"

	ApplicationKindMarketRequester ApplicationKind = "MarketRequester"

	ApplicationKindGateway ApplicationKind = "Gateway"
)

type ApplicationPhase string

const (
	ApplicationPhasePending ApplicationPhase = "Pending"

	ApplicationPhaseProvisioning ApplicationPhase = "Provisioning"

	ApplicationPhaseReady ApplicationPhase = "Ready"

	ApplicationPhaseDeleting ApplicationPhase = "Deleting"

	ApplicationPhaseFailed ApplicationPhase = "Failed"
)

type ApplicationOption struct {
	Name string `json:"name"`

	Value string `json:"value,omitempty"`
}

type ApplicationIdentity struct {
	Mode MarketIdentityMode `json:"mode"`

	Handle string `json:"handle,omitempty"`

	PdsUrl string `json:"pdsUrl,omitempty"`
}

type FirecrackerProvider struct {
	WorkRoot string `json:"workRoot"`

	VmruntimeCommand []string `json:"vmruntimeCommand"`
}

type ApplicationListener struct {
	Component string `json:"component"`

	Name string `json:"name"`

	Kind string `json:"kind"`

	Address string `json:"address"`
}

type ApplicationComponent struct {
	Kind string `json:"kind"`

	Name string `json:"name"`

	Workspace string `json:"workspace"`

	Phase string `json:"phase,omitempty"`

	Ready bool `json:"ready"`
}

type ApplicationStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase ApplicationPhase `json:"phase,omitempty"`

	Message string `json:"message,omitempty"`

	Profile string `json:"profile,omitempty"`

	Did string `json:"did,omitempty"`

	Components []ApplicationComponent `json:"components,omitempty"`

	Listeners []ApplicationListener `json:"listeners,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type MarketBidder struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MarketBidderSpec  `json:"spec,omitempty"`
	Status ApplicationStatus `json:"status,omitempty"`
}

type MarketBidderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MarketBidder `json:"items"`
}

type MarketBidderSpec struct {
	Profile string `json:"profile"`

	Network *MarketNetworkValue `json:"network,omitempty"`

	Identity ApplicationIdentity `json:"identity"`

	Policy string `json:"policy,omitempty"`

	Firecracker *FirecrackerProvider `json:"firecracker,omitempty"`

	Options []ApplicationOption `json:"options,omitempty"`
}

type MarketRequester struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MarketRequesterSpec `json:"spec,omitempty"`
	Status ApplicationStatus   `json:"status,omitempty"`
}

type MarketRequesterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MarketRequester `json:"items"`
}

type MarketRequesterSpec struct {
	Profile string `json:"profile"`

	Network *MarketNetworkValue `json:"network,omitempty"`

	Identity ApplicationIdentity `json:"identity"`

	BidderDids []string `json:"bidderDids,omitempty"`

	Options []ApplicationOption `json:"options,omitempty"`
}

type Gateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GatewaySpec       `json:"spec,omitempty"`
	Status ApplicationStatus `json:"status,omitempty"`
}

type GatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Gateway `json:"items"`
}

type GatewaySpec struct {
	Profile string `json:"profile"`

	Network *MarketNetworkValue `json:"network,omitempty"`

	BidderDids []string `json:"bidderDids,omitempty"`

	Options []ApplicationOption `json:"options,omitempty"`
}

type MarketNetwork struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MarketNetworkSpec   `json:"spec,omitempty"`
	Status MarketNetworkStatus `json:"status,omitempty"`
}

type MarketNetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MarketNetwork `json:"items"`
}

type MarketNetworkSpec struct {
	Profile string `json:"profile"`

	GuestLabel string `json:"guestLabel,omitempty"`

	Apps []NetworkApp `json:"apps,omitempty"`
}

type NetworkApp struct {
	Name string `json:"name"`

	Kind ApplicationKind `json:"kind"`

	Profile string `json:"profile"`

	Policy string `json:"policy,omitempty"`

	Firecracker *FirecrackerProvider `json:"firecracker,omitempty"`

	Options []ApplicationOption `json:"options,omitempty"`
}

type NetworkAppStatus struct {
	Name string `json:"name"`

	Kind ApplicationKind `json:"kind"`

	Instance string `json:"instance,omitempty"`

	KcpUrl string `json:"kcpUrl,omitempty"`

	KcpRoot string `json:"kcpRoot,omitempty"`

	Phase ApplicationPhase `json:"phase,omitempty"`

	Message string `json:"message,omitempty"`

	Did string `json:"did,omitempty"`

	Listeners []ApplicationListener `json:"listeners,omitempty"`
}

type MarketNetworkStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase ApplicationPhase `json:"phase,omitempty"`

	Message string `json:"message,omitempty"`

	Network *MarketNetworkValue `json:"network,omitempty"`

	Components []ApplicationComponent `json:"components,omitempty"`

	Listeners []ApplicationListener `json:"listeners,omitempty"`

	Apps []NetworkAppStatus `json:"apps,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
