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

	Hosts []NetworkHost `json:"hosts,omitempty"`
}

type NetworkHost struct {
	Name string `json:"name"`

	Address string `json:"address"`
}

type ApplicationKind string

const (
	ApplicationKindMarketBidder ApplicationKind = "MarketBidder"

	ApplicationKindMarketRequester ApplicationKind = "MarketRequester"

	ApplicationKindGateway ApplicationKind = "Gateway"

	ApplicationKindSshGateway ApplicationKind = "SshGateway"
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

	Association AssociationMode `json:"association,omitempty"`
}

type FirecrackerProvider struct {
	WorkRoot string `json:"workRoot"`

	VmruntimeCommand []string `json:"vmruntimeCommand"`
}

type KcpProvider struct {
	Handle string `json:"handle,omitempty"`

	VmHostBackend string `json:"vmHostBackend,omitempty"`

	GuestImageSource string `json:"guestImageSource"`

	StopGraceSeconds int32 `json:"stopGraceSeconds,omitempty"`
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

	StateDir *StateDirUsage `json:"stateDir,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type StateDirUsage struct {
	Path string `json:"path"`

	Bytes int64 `json:"bytes"`

	Files int64 `json:"files"`

	SampledAt string `json:"sampledAt"`
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

	Kcp *KcpProvider `json:"kcp,omitempty"`

	SigningKeyRotation int32 `json:"signingKeyRotation,omitempty"`

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

	MarketNetwork *GatewayMarketNetwork `json:"marketNetwork,omitempty"`

	WebAppFront string `json:"webAppFront,omitempty"`
}

type GatewayMarketNetwork struct {
	OwnerSessionsFile string `json:"ownerSessionsFile,omitempty"`

	NodeGuestHostAliases []string `json:"nodeGuestHostAliases,omitempty"`

	NodeUserDataBaseFile string `json:"nodeUserDataBaseFile,omitempty"`
}

type SshGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SshGatewaySpec    `json:"spec,omitempty"`
	Status ApplicationStatus `json:"status,omitempty"`
}

type SshGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []SshGateway `json:"items"`
}

type SshGatewaySpec struct {
	Profile string `json:"profile"`

	Network *MarketNetworkValue `json:"network,omitempty"`

	BidderDids []string `json:"bidderDids,omitempty"`

	Options []ApplicationOption `json:"options,omitempty"`

	OwnerSessionsFile string `json:"ownerSessionsFile,omitempty"`
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

	Kcp *KcpProvider `json:"kcp,omitempty"`

	Options []ApplicationOption `json:"options,omitempty"`

	OwnerSessionsFile string `json:"ownerSessionsFile,omitempty"`

	Stopped bool `json:"stopped,omitempty"`
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

	Components []ApplicationComponent `json:"components,omitempty"`

	Listeners []ApplicationListener `json:"listeners,omitempty"`
}

type MarketNetworkStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase ApplicationPhase `json:"phase,omitempty"`

	Message string `json:"message,omitempty"`

	Network *MarketNetworkValue `json:"network,omitempty"`

	BidderPdsUrl string `json:"bidderPdsUrl,omitempty"`

	Components []ApplicationComponent `json:"components,omitempty"`

	Listeners []ApplicationListener `json:"listeners,omitempty"`

	Apps []NetworkAppStatus `json:"apps,omitempty"`

	StateDir *StateDirUsage `json:"stateDir,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
