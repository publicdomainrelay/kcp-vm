package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type OpenBaoServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenBaoServerSpec   `json:"spec,omitempty"`
	Status OpenBaoServerStatus `json:"status,omitempty"`
}

type OpenBaoServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []OpenBaoServer `json:"items"`
}

type OpenBaoServerSpec struct {
	Asset *HostProcessAsset `json:"asset,omitempty"`

	Exec string `json:"exec,omitempty"`

	Listen string `json:"listen,omitempty"`

	Init OpenBaoServerInit `json:"init,omitempty"`

	RestartPolicy HostProcessRestartPolicy `json:"restartPolicy,omitempty"`
}

type OpenBaoServerInit struct {
	Shares int32 `json:"shares,omitempty"`

	Threshold int32 `json:"threshold,omitempty"`
}

type OpenBaoServerPhase string

const (
	OpenBaoServerPhasePending OpenBaoServerPhase = "Pending"

	OpenBaoServerPhaseStarting OpenBaoServerPhase = "Starting"

	OpenBaoServerPhaseSealed OpenBaoServerPhase = "Sealed"

	OpenBaoServerPhaseReady OpenBaoServerPhase = "Ready"

	OpenBaoServerPhaseFailed OpenBaoServerPhase = "Failed"
)

type OpenBaoServerStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase OpenBaoServerPhase `json:"phase,omitempty"`

	Message string `json:"message,omitempty"`

	Address string `json:"address,omitempty"`

	Initialized bool `json:"initialized"`

	Sealed bool `json:"sealed"`

	HostProcess string `json:"hostProcess,omitempty"`

	Runtime *OpenBaoServerRuntime `json:"runtime,omitempty"`

	UnsealedInstance string `json:"unsealedInstance,omitempty"`

	UnsealedAt string `json:"unsealedAt,omitempty"`

	Refs *OpenBaoServerRefs `json:"refs,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type OpenBaoServerRuntime struct {
	Backend string `json:"backend,omitempty"`

	Ref string `json:"ref,omitempty"`

	Instance string `json:"instance,omitempty"`

	Pid int64 `json:"pid,omitempty"`

	StartTime string `json:"startTime,omitempty"`

	HostBootID string `json:"hostBootID,omitempty"`
}

type OpenBaoServerRefs struct {
	UnsealKeys string `json:"unsealKeys,omitempty"`

	CA string `json:"ca,omitempty"`

	ClientCert string `json:"clientCert,omitempty"`

	ClientKey string `json:"clientKey,omitempty"`

	CertAuthMount string `json:"certAuthMount,omitempty"`

	CertAuthRole string `json:"certAuthRole,omitempty"`

	KVMount string `json:"kvMount,omitempty"`
}
