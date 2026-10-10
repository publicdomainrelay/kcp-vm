package clusterstack

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

var ErrNotAProcess = errors.New(
	"clusterstack: embedded-postgres is a Go library, not a binary; the component that " +
		"links it starts it via the library's own Run/Stop and supplies PostgresPort")

var ErrMissingTokenFile = errors.New(
	"clusterstack: TokenFile is required; k3s derives its bootstrap storage key from the " +
		"instance token, so a k3s that mints its own breaks the link instance.PlanFor decided")

type Spec struct {
	DataDir string

	Cluster string

	PostgresPort int

	OpenBaoConfig string

	OpenBaoPort int

	OpenBaoTLSCertFile string

	OpenBaoTLSKeyFile string

	OpenBaoPostgresDSN string

	StoreEndpoint string

	KineListen string

	DatastoreEndpoint string

	AdvertiseAddress string

	BindAddress string

	GuestDialled bool

	MarketDialled bool

	TLSSANs []string

	AdvertisePort int

	K3sSecurePort int

	K3sControllerManagerPort      int
	K3sSchedulerPort              int
	K3sCloudControllerManagerPort int

	TokenFile string

	CloudControllerManagerBinary string

	ExternalSecrets ExternalSecrets

	ServiceAccountIssuer string
	APIAudiences         []string

	Audit Audit
}

type ExternalSecrets struct {
	Binary string

	HealthPort int

	WebhookPort int

	WebhookHealth int
}

const ExternalSecretsWebhookLookahead = 720 * time.Hour

type Audit struct {
	PolicyPath string

	LogPath string

	MaxSizeMB  int
	MaxAgeDays int
	MaxBackups int
}

const (
	DefaultAuditMaxSizeMB  = 100
	DefaultAuditMaxAgeDays = 0
	DefaultAuditMaxBackups = 0
)

var (
	ErrNoAuditPolicy = errors.New("clusterstack: Audit.PolicyPath is required")
	ErrNoAuditLog    = errors.New("clusterstack: Audit.LogPath is required")
)

func (a Audit) Validate() error {
	switch {
	case a.PolicyPath == "":
		return ErrNoAuditPolicy
	case a.LogPath == "":
		return ErrNoAuditLog
	}
	return nil
}

func (a Audit) argv() []string {
	size, age, backups := a.MaxSizeMB, a.MaxAgeDays, a.MaxBackups
	if size == 0 {
		size = DefaultAuditMaxSizeMB
	}
	if age == 0 {
		age = DefaultAuditMaxAgeDays
	}
	if backups == 0 {
		backups = DefaultAuditMaxBackups
	}
	return []string{
		"--kube-apiserver-arg=audit-policy-file=" + a.PolicyPath,
		"--kube-apiserver-arg=audit-log-path=" + a.LogPath,
		"--kube-apiserver-arg=audit-log-maxsize=" + strconv.Itoa(size),
		"--kube-apiserver-arg=audit-log-maxage=" + strconv.Itoa(age),
		"--kube-apiserver-arg=audit-log-maxbackup=" + strconv.Itoa(backups),
	}
}

var (
	ErrMissingDataDir    = errors.New("clusterstack: DataDir is required")
	ErrMissingPort       = errors.New("clusterstack: PostgresPort is required")
	ErrMissingEndpoint   = errors.New("clusterstack: StoreEndpoint is required")
	ErrMissingKineListen = errors.New("clusterstack: KineListen is required: kine defaults to " +
		"0.0.0.0:2379, so the first cluster takes it and every later one FATALs")
	ErrMissingDatastoreEndpoint = errors.New("clusterstack: DatastoreEndpoint is required: k3s " +
		"resolves an empty --datastore-endpoint to its own sqlite and starts a NEW cluster, " +
		"silently -- wrong CA, none of the existing objects")
	ErrMissingAdvertise   = errors.New("clusterstack: AdvertiseAddress and AdvertisePort are both required")
	ErrMissingBindAddress = errors.New("clusterstack: BindAddress is required and is NOT the " +
		"advertise address: the apiserver REJECTS a loopback address for the in-cluster kubernetes " +
		"Service endpoint (measured: subsets[0].addresses[0].ip Invalid value: \"127.0.0.1\": may " +
		"not be in the loopback range), so the advertised address is one the host carries while the " +
		"TCP listener stays off the network. Binding what is advertised is how every tenant's " +
		"apiserver ends up on every interface of the host")
	ErrMissingK3sSecurePort = errors.New("clusterstack: K3sSecurePort is required: k3s's " +
		"--https-listen-port defaults to 6443, ONE PORT FOR THE WHOLE HOST, so the first " +
		"cluster takes it and every later one fails at bind")
	ErrMissingK3sControlPlanePort = errors.New("clusterstack: the k3s control-plane " +
		"component secure ports are required: kube-controller-manager, kube-scheduler and " +
		"the cloud-controller-manager default to 10257, 10259 and 10258 on 127.0.0.1 -- one " +
		"set for the whole host")
	ErrMissingCloudControllerManagerBinary = errors.New("clusterstack: " +
		"CloudControllerManagerBinary is required: the cloud controller manager is an out-of-cluster " +
		"controller dialling the apiserver with a minted tenant credential, and nothing on PATH or " +
		"derived from the data dir names that binary. A cluster that came up without it would have " +
		"Nodes carrying no providerID and nothing reconciling them")
	ErrMissingExternalSecrets = errors.New("clusterstack: the External Secrets controller and its " +
		"webhook run beside the control plane, one pair per cluster, and each needs the pinned " +
		"external-secrets binary and its own leased ports: the upstream defaults (:8080 metrics, " +
		":8081 webhook health, :8082 controller health, 10250 webhook) are one set for the whole " +
		"host, so the second cluster's pair would fail at bind")
	ErrTwoDialAddresses = errors.New("clusterstack: GuestDialled and MarketDialled are both set, and " +
		"they name two different answers to where a node's machine is. A guest reaches this host " +
		"through the gateway of the range its cluster holds in the subnet lease file and a market " +
		"node reaches it through the market's SSH door, so a stack carrying both would be checked " +
		"against one of them and advertise for the other")
)

func (s Spec) Validate() error {
	switch {
	case s.DataDir == "":
		return ErrMissingDataDir
	case s.PostgresPort <= 0 || s.PostgresPort > 65535:
		return fmt.Errorf("%w (got %d)", ErrMissingPort, s.PostgresPort)
	case s.StoreEndpoint == "":
		return ErrMissingEndpoint
	case s.KineListen == "":
		return ErrMissingKineListen
	case s.DatastoreEndpoint == "":
		return ErrMissingDatastoreEndpoint
	case s.AdvertiseAddress == "" || s.AdvertisePort <= 0 || s.AdvertisePort > 65535:
		return ErrMissingAdvertise
	case s.BindAddress == "":
		return ErrMissingBindAddress
	case s.K3sSecurePort <= 0 || s.K3sSecurePort > 65535:
		return fmt.Errorf("%w (got %d)", ErrMissingK3sSecurePort, s.K3sSecurePort)
	case s.K3sControllerManagerPort <= 0 || s.K3sControllerManagerPort > 65535:
		return fmt.Errorf("%w (%s got %d)", ErrMissingK3sControlPlanePort,
			"K3sControllerManagerPort", s.K3sControllerManagerPort)
	case s.K3sSchedulerPort <= 0 || s.K3sSchedulerPort > 65535:
		return fmt.Errorf("%w (%s got %d)", ErrMissingK3sControlPlanePort,
			"K3sSchedulerPort", s.K3sSchedulerPort)
	case s.K3sCloudControllerManagerPort <= 0 || s.K3sCloudControllerManagerPort > 65535:
		return fmt.Errorf("%w (%s got %d)", ErrMissingK3sControlPlanePort,
			"K3sCloudControllerManagerPort", s.K3sCloudControllerManagerPort)
	case s.TokenFile == "":
		return ErrMissingTokenFile
	case s.GuestDialled && s.MarketDialled:
		return ErrTwoDialAddresses
	}
	return s.Audit.Validate()
}

func (s Spec) Argv(c procgroup.Component) ([]string, error) {
	switch c {
	case procgroup.ComponentPostgres:
		return nil, ErrNotAProcess
	case procgroup.ComponentOpenBao:
		return s.OpenBaoArgv()
	case procgroup.ComponentKine:
		return s.KineArgv()
	case procgroup.ComponentK3s:
		return s.ServerArgv()
	case procgroup.ComponentCloudControllerManager:
		return s.CloudControllerManagerArgv()
	case procgroup.ComponentExternalSecrets:
		return s.ExternalSecretsArgv()
	case procgroup.ComponentExternalSecretsWebhook:
		return s.ExternalSecretsWebhookArgv()
	}
	return nil, fmt.Errorf("clusterstack: unknown component %q", c)
}

func (s Spec) CloudControllerManagerArgv() ([]string, error) {
	if s.CloudControllerManagerBinary == "" {
		return nil, ErrMissingCloudControllerManagerBinary
	}
	return []string{s.CloudControllerManagerBinary}, nil
}

func validPort(p int) bool { return p > 0 && p <= 65535 }

func (s Spec) ExternalSecretsArgv() ([]string, error) {
	e := s.ExternalSecrets
	if e.Binary == "" || !validPort(e.HealthPort) {
		return nil, fmt.Errorf("%w (binary %q, health port %d)", ErrMissingExternalSecrets, e.Binary, e.HealthPort)
	}
	return []string{
		e.Binary,
		"--live-addr=" + net.JoinHostPort("127.0.0.1", strconv.Itoa(e.HealthPort)),
		"--metrics-addr=0",
		"--enable-leader-election=false",
		"--zap-time-encoding=iso8601",
	}, nil
}

func (s Spec) ExternalSecretsWebhookArgv() ([]string, error) {
	e := s.ExternalSecrets
	if e.Binary == "" || !validPort(e.WebhookPort) || !validPort(e.WebhookHealth) || s.DataDir == "" {
		return nil, fmt.Errorf("%w (binary %q, webhook port %d, webhook health port %d, data dir %q)",
			ErrMissingExternalSecrets, e.Binary, e.WebhookPort, e.WebhookHealth, s.DataDir)
	}
	return []string{
		e.Binary, "webhook",
		"--port=" + strconv.Itoa(e.WebhookPort),
		"--healthz-addr=" + net.JoinHostPort("127.0.0.1", strconv.Itoa(e.WebhookHealth)),
		"--metrics-addr=0",
		"--cert-dir=" + ExternalSecretsCertDir(s.DataDir),
		"--dns-name=localhost",
		"--lookahead-interval=" + ExternalSecretsWebhookLookahead.String(),
		"--check-interval=1h0m0s",
		"--zap-time-encoding=iso8601",
	}, nil
}

const MarketClusterCIDR = "10.42.0.0/16"

const MarketFlannelBackend = "none"

func (s Spec) podNetworkArgv() []string {
	if !s.MarketDialled {
		return nil
	}
	return []string{
		"--flannel-backend=" + MarketFlannelBackend,
		"--cluster-cidr=" + MarketClusterCIDR,
	}
}

func (s Spec) ServerArgv() ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	argv := []string{
		"k3s", "server",
		"--disable-agent",
		"--disable=traefik",
		"--data-dir", s.DataDir,
		"--datastore-endpoint", s.DatastoreEndpoint,
		"--egress-selector-mode", "cluster",
		"--advertise-address", s.AdvertiseAddress,
		"--advertise-port", strconv.Itoa(s.AdvertisePort),
		"--bind-address", s.BindAddress,
		"--https-listen-port", strconv.Itoa(s.K3sSecurePort),
		"--token-file", s.TokenFile,
		"--write-kubeconfig", AdminKubeconfig(s.DataDir),
	}
	for _, san := range s.TLSSANs {
		argv = append(argv, "--tls-san", san)
	}
	argv = append(argv, s.podNetworkArgv()...)
	argv = append(argv, s.Audit.argv()...)
	if s.ServiceAccountIssuer != "" {
		argv = append(argv, "--kube-apiserver-arg=service-account-issuer="+s.ServiceAccountIssuer)
	}
	if len(s.APIAudiences) > 0 {
		argv = append(argv, "--kube-apiserver-arg=api-audiences="+strings.Join(s.APIAudiences, ","))
	}
	argv = append(argv,
		"--kube-controller-manager-arg=secure-port="+strconv.Itoa(s.K3sControllerManagerPort),
		"--kube-scheduler-arg=secure-port="+strconv.Itoa(s.K3sSchedulerPort),
		"--kube-cloud-controller-manager-arg=secure-port="+strconv.Itoa(s.K3sCloudControllerManagerPort),
	)
	return argv, nil
}

func (s Spec) KineArgv() ([]string, error) {
	if s.StoreEndpoint == "" {
		return nil, ErrMissingEndpoint
	}
	return []string{
		"kine",
		"--endpoint", s.StoreEndpoint,
		"--listen-address", s.KineListen,
		"--metrics-bind-address=0",
	}, nil
}

func (s Spec) OpenBaoArgv() ([]string, error) {
	if s.OpenBaoConfig == "" {
		return nil, errors.New("clusterstack: OpenBaoConfig is required")
	}
	return []string{"bao", "server", "-config=" + s.OpenBaoConfig}, nil
}

type Postgres struct {
	Port    int
	DataDir string
}

func (s Spec) PostgresConfig() (Postgres, error) {
	if err := s.Validate(); err != nil {
		return Postgres{}, err
	}
	return Postgres{
		Port:    s.PostgresPort,
		DataDir: filepath.Join(s.DataDir, "postgres"),
	}, nil
}

func TLSDir(dataDir string) string { return filepath.Join(dataDir, "server", "tls") }

func CredDir(dataDir string) string { return filepath.Join(dataDir, "server", "cred") }

const AdminKubeconfigName = "admin.kubeconfig"

func AdminKubeconfig(dataDir string) string {
	return filepath.Join(CredDir(dataDir), AdminKubeconfigName)
}

const CloudControllerManagerKubeconfigName = "cloud-controller-manager.kubeconfig"

func CloudControllerManagerKubeconfig(dataDir string) string {
	return filepath.Join(dataDir, CloudControllerManagerKubeconfigName)
}

const ExternalSecretsKubeconfigName = "external-secrets.kubeconfig"

func ExternalSecretsKubeconfig(dataDir string) string {
	return filepath.Join(dataDir, ExternalSecretsKubeconfigName)
}

func ExternalSecretsDir(dataDir string) string { return filepath.Join(dataDir, "external-secrets") }

func ExternalSecretsCertDir(dataDir string) string {
	return filepath.Join(ExternalSecretsDir(dataDir), "webhook-certs")
}

func ExternalSecretsCAKey(dataDir string) string {
	return filepath.Join(ExternalSecretsDir(dataDir), "webhook-ca.key")
}

const NodeSnapshotName = "nodes.json"

func NodeSnapshotPath(dataDir string) string {
	return filepath.Join(dataDir, NodeSnapshotName)
}
