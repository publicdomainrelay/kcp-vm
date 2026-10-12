package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type PolicyEngine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolicyEngineSpec   `json:"spec,omitempty"`
	Status PolicyEngineStatus `json:"status,omitempty"`
}

type PolicyEngineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []PolicyEngine `json:"items"`
}

type PolicyEngineSpec struct {
	DenoJSON runtime.RawExtension `json:"denoJson,omitempty"`

	DenoLock string `json:"denoLock,omitempty"`

	Permissions *DenoPermissions `json:"permissions,omitempty"`

	ServiceAccount *ServiceAccountRef `json:"serviceAccount,omitempty"`

	Env map[string]string `json:"env,omitempty"`

	RestartPolicy DenoRestartPolicy `json:"restartPolicy,omitempty"`

	ReadinessProbe *ExecProbe `json:"readinessProbe,omitempty"`

	LivenessProbe *ExecProbe `json:"livenessProbe,omitempty"`
}

type PolicyEnginePhase string

const (
	PolicyEnginePending PolicyEnginePhase = "Pending"

	PolicyEngineRunning PolicyEnginePhase = "Running"

	PolicyEngineFailed PolicyEnginePhase = "Failed"
)

type PolicyEngineStatus struct {
	Phase PolicyEnginePhase `json:"phase,omitempty"`

	RunID string `json:"runID,omitempty"`

	Endpoint string `json:"endpoint,omitempty"`

	Restarts int32 `json:"restarts,omitempty"`

	Ready bool `json:"ready,omitempty"`

	StartTime *metav1.Time `json:"startTime,omitempty"`

	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	Message string `json:"message,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const FinalizerPolicyEngine = "policyengine.deno.computer/run"
