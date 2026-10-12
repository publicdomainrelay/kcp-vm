package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Postgres struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostgresSpec   `json:"spec,omitempty"`
	Status PostgresStatus `json:"status,omitempty"`
}

type PostgresList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Postgres `json:"items"`
}

type PostgresSpec struct {
	Version string `json:"version,omitempty"`

	DataDir string `json:"dataDir,omitempty"`

	Databases []string `json:"databases,omitempty"`

	Settings []string `json:"settings,omitempty"`

	DeleteData bool `json:"deleteData,omitempty"`
}

type PostgresPhase string

const (
	PostgresPhasePending PostgresPhase = "Pending"

	PostgresPhaseStarting PostgresPhase = "Starting"

	PostgresPhaseReady PostgresPhase = "Ready"

	PostgresPhaseFailed PostgresPhase = "Failed"

	PostgresPhaseDeleting PostgresPhase = "Deleting"
)

type PostgresStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Phase PostgresPhase `json:"phase,omitempty"`

	Message string `json:"message,omitempty"`

	Version string `json:"version,omitempty"`

	DataDir string `json:"dataDir,omitempty"`

	SocketDir string `json:"socketDir,omitempty"`

	User string `json:"user,omitempty"`

	DSN string `json:"dsn,omitempty"`

	HostProcess string `json:"hostProcess,omitempty"`

	Runtime *HostProcessRuntime `json:"runtime,omitempty"`

	ReadyAt string `json:"readyAt,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
