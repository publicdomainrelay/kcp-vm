package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
)

type ServiceAccountRef struct {
	Name string `json:"name"`

	Namespace string `json:"namespace,omitempty"`
}

type DenoPermission struct {
	Allow bool `json:"allow,omitempty"`

	AllowList []string `json:"allowList,omitempty"`

	Deny bool `json:"deny,omitempty"`

	DenyList []string `json:"denyList,omitempty"`
}

type DenoPermissions struct {
	All bool `json:"all,omitempty"`

	NoPrompt bool `json:"noPrompt,omitempty"`

	HRTime bool `json:"hrtime,omitempty"`

	Read *DenoPermission `json:"read,omitempty"`

	Write *DenoPermission `json:"write,omitempty"`

	Net *DenoPermission `json:"net,omitempty"`

	Env *DenoPermission `json:"env,omitempty"`

	Run *DenoPermission `json:"run,omitempty"`

	FFI *DenoPermission `json:"ffi,omitempty"`

	Sys *DenoPermission `json:"sys,omitempty"`

	Import *DenoPermission `json:"import,omitempty"`

	IgnoreEnv *DenoPermission `json:"ignoreEnv,omitempty"`

	AllowScripts []string `json:"allowScripts,omitempty"`
}

type DenoPodTemplate struct {
	DenoJSON runtime.RawExtension `json:"denoJson,omitempty"`

	DenoLock string `json:"denoLock,omitempty"`

	Script string `json:"script"`

	Permissions *DenoPermissions `json:"permissions,omitempty"`

	ServiceAccount *ServiceAccountRef `json:"serviceAccount,omitempty"`

	APIServer string `json:"apiServer,omitempty"`

	Env map[string]string `json:"env,omitempty"`
}

type ExecProbe struct {
	Command []string `json:"command,omitempty"`

	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`

	FailureThreshold *int32 `json:"failureThreshold,omitempty"`

	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

type DenoRestartPolicy string

const (
	RestartAlways DenoRestartPolicy = "Always"

	RestartOnFailure DenoRestartPolicy = "OnFailure"

	RestartNever DenoRestartPolicy = "Never"
)

type ConcurrencyPolicy string

const (
	ConcurrencyAllow ConcurrencyPolicy = "Allow"

	ConcurrencyForbid ConcurrencyPolicy = "Forbid"

	ConcurrencyReplace ConcurrencyPolicy = "Replace"
)

const (
	ConditionReady = "Ready"

	ConditionComplete = "Complete"

	ConditionFailed = "Failed"

	ConditionSuspended = "Suspended"

	ConditionCancelled = "Cancelled"
)
