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

	Clients []OpenBaoServerClient `json:"clients,omitempty"`

	Unit string `json:"unit,omitempty"`

	Slice string `json:"slice,omitempty"`

	After []string `json:"after,omitempty"`

	Wants []string `json:"wants,omitempty"`

	Log string `json:"log,omitempty"`

	Storage *OpenBaoServerStorage `json:"storage,omitempty"`

	TLS *OpenBaoServerTLS `json:"tls,omitempty"`

	Declared *OpenBaoServerDeclared `json:"declared,omitempty"`

	Plugins []OpenBaoPlugin `json:"plugins,omitempty"`
}

type OpenBaoPlugin struct {
	Name string `json:"name"`

	Type string `json:"type"`

	Asset HostProcessAsset `json:"asset"`

	MountPath string `json:"mountPath,omitempty"`
}

type OpenBaoServerClient struct {
	Name string `json:"name"`

	KVMount string `json:"kvMount,omitempty"`

	Policies []string `json:"policies,omitempty"`

	TokenTTL string `json:"tokenTTL,omitempty"`

	TokenMaxTTL string `json:"tokenMaxTTL,omitempty"`
}

type OpenBaoServerStorage struct {
	Postgres *OpenBaoServerPostgres `json:"postgres,omitempty"`
}

type OpenBaoServerPostgres struct {
	SocketDir string `json:"socketDir"`

	User string `json:"user"`

	Database string `json:"database"`
}

type OpenBaoServerTLS struct {
	CertFile string `json:"certFile"`

	KeyFile string `json:"keyFile"`

	CAFile string `json:"caFile"`

	ClientCertOptional bool `json:"clientCertOptional,omitempty"`
}

type OpenBaoServerDeclared struct {
	SecretMounts []OpenBaoSecretMount `json:"secretMounts,omitempty"`

	AuthMounts []OpenBaoAuthMount `json:"authMounts,omitempty"`

	Policies []OpenBaoPolicy `json:"policies,omitempty"`

	CertRoles []OpenBaoCertRole `json:"certRoles,omitempty"`

	JWTRoles []OpenBaoJWTRole `json:"jwtRoles,omitempty"`

	AppRoles []OpenBaoAppRole `json:"appRoles,omitempty"`

	PKIRoles []OpenBaoPKIRole `json:"pkiRoles,omitempty"`

	KV []OpenBaoKVScope `json:"kv,omitempty"`
}

type OpenBaoSecretMount struct {
	Path string `json:"path"`

	Type string `json:"type"`

	MaxLeaseTTL string `json:"maxLeaseTTL,omitempty"`
}

type OpenBaoAuthMount struct {
	Path string `json:"path"`

	Type string `json:"type"`

	JWT *OpenBaoJWTConfig `json:"jwt,omitempty"`
}

type OpenBaoJWTConfig struct {
	DiscoveryURL string `json:"discoveryURL"`

	BoundIssuer string `json:"boundIssuer,omitempty"`
}

type OpenBaoPolicy struct {
	Name string `json:"name"`

	Rules []OpenBaoPolicyRule `json:"rules,omitempty"`

	HCL string `json:"hcl,omitempty"`
}

type OpenBaoPolicyRule struct {
	Path string `json:"path"`

	Capabilities []string `json:"capabilities"`
}

type OpenBaoCertRole struct {
	Mount string `json:"mount"`

	Name string `json:"name"`

	CommonNames []string `json:"commonNames"`

	CAFromPKI string `json:"caFromPKI"`

	Policies []string `json:"policies"`

	TokenTTL string `json:"tokenTTL,omitempty"`

	TokenMaxTTL string `json:"tokenMaxTTL,omitempty"`

	NoDefaultPolicy bool `json:"noDefaultPolicy,omitempty"`
}

type OpenBaoJWTRole struct {
	Mount string `json:"mount"`

	Name string `json:"name"`

	RoleType string `json:"roleType,omitempty"`

	BoundSubject string `json:"boundSubject"`

	BoundAudiences []string `json:"boundAudiences"`

	UserClaim string `json:"userClaim"`

	Policies []string `json:"policies"`

	TokenTTL string `json:"tokenTTL,omitempty"`
}

type OpenBaoAppRole struct {
	Mount string `json:"mount,omitempty"`

	Name string `json:"name"`

	Policies []string `json:"policies"`

	TokenTTL string `json:"tokenTTL,omitempty"`

	TokenMaxTTL string `json:"tokenMaxTTL,omitempty"`

	SecretIDTTL string `json:"secretIDTTL,omitempty"`
}

type OpenBaoPKIRole struct {
	Mount string `json:"mount"`

	Name string `json:"name"`

	AllowedDomains []string `json:"allowedDomains,omitempty"`

	AllowSubdomains bool `json:"allowSubdomains,omitempty"`

	AllowBareDomains bool `json:"allowBareDomains,omitempty"`

	AllowAnyName bool `json:"allowAnyName,omitempty"`

	AllowIPSANs bool `json:"allowIPSANs,omitempty"`

	EnforceHostnames bool `json:"enforceHostnames,omitempty"`

	RequireCN bool `json:"requireCN,omitempty"`

	ClientFlag bool `json:"clientFlag,omitempty"`

	ServerFlag bool `json:"serverFlag,omitempty"`

	KeyType string `json:"keyType,omitempty"`

	KeyBits int32 `json:"keyBits,omitempty"`

	KeyUsage []string `json:"keyUsage,omitempty"`

	Organization []string `json:"organization,omitempty"`

	CNValidations []string `json:"cnValidations,omitempty"`

	TTL string `json:"ttl,omitempty"`

	MaxTTL string `json:"maxTTL,omitempty"`
}

type OpenBaoKVScope struct {
	Mount string `json:"mount"`

	Prefix string `json:"prefix,omitempty"`

	Keys []string `json:"keys,omitempty"`

	Grace string `json:"grace,omitempty"`
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

	Clients []OpenBaoServerClientRefs `json:"clients,omitempty"`

	Sync *OpenBaoServerSync `json:"sync,omitempty"`

	Plugins []OpenBaoServerPluginStatus `json:"plugins,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type OpenBaoServerSync struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	SyncedAt string `json:"syncedAt,omitempty"`

	LastChangeAt string `json:"lastChangeAt,omitempty"`

	LastChange string `json:"lastChange,omitempty"`

	Declared string `json:"declared,omitempty"`

	Pending []string `json:"pending,omitempty"`

	Conflicts []string `json:"conflicts,omitempty"`
}

type OpenBaoServerClientRefs struct {
	Name string `json:"name"`

	CertAuthMount string `json:"certAuthMount,omitempty"`

	CertAuthRole string `json:"certAuthRole,omitempty"`

	Policy string `json:"policy,omitempty"`

	KVMount string `json:"kvMount,omitempty"`

	ClientCert string `json:"clientCert,omitempty"`

	ClientKey string `json:"clientKey,omitempty"`
}

type OpenBaoServerPluginStatus struct {
	Name string `json:"name"`

	Type string `json:"type"`

	Sha256 string `json:"sha256,omitempty"`

	Command string `json:"command,omitempty"`

	MountPath string `json:"mountPath,omitempty"`

	RunningSha256 string `json:"runningSha256,omitempty"`

	Message string `json:"message,omitempty"`
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
