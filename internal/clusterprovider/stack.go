package clusterprovider

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

type VaultTLS struct {
	CertFile string

	KeyFile string

	CAPEM []byte
}

var ErrNoVaultTLS = errors.New(
	"clusterprovider: the vault's listener is loopback-only and TLS, and no per-cluster " +
		"certificate source was supplied. A listener with no certificate cannot present one, so " +
		"auth/cert can never answer on it and every guest's and every admin's call fails with a " +
		"TLS connection error that names nothing")

const TokenFileName = "token"

func TokenPath(dataDir string) string { return filepath.Join(dataDir, TokenFileName) }

func DaemonURL(kineListen string) string { return "http://" + kineListen }

var ErrNoStore = errors.New(
	"clusterprovider: no store endpoint for this cluster, and kine is the only thing k3s dials. " +
		"k3s is NOT pointed at the bucket: measured against k3s v1.36.4+k3s1, the S3 form is refused " +
		"at startup with 'unknown driver' and a t4 endpoint makes k3s start its OWN kine against the " +
		"same bucket instead of dialing ours, which is two writers on one store")

var ErrNoGuardSeam = errors.New(
	"clusterprovider: this Stack puts no step before k3s, so materialiser.guardBeforeK3s " +
		"would return the steps unchanged and the identity guard would SILENTLY NOT RUN -- " +
		"a skipped guard and a satisfied one look identical from a green suite")

var ErrOpenBaoConfigUnwritable = errors.New(
	"clusterprovider: the OpenBao config could not be put on disk before the vault step ran. " +
		"bao server takes exactly one config file and refuses to come up without it -- measured on " +
		"v2.6.2, exit 1 with 'error loading configuration from <path>: stat <path>: no such file or " +
		"directory' -- so a write that failed quietly would leave the vault failing at start for a " +
		"reason that is not in the vault's own log")

const openBaoConfigMode = 0o600

type AdminClient func(ctx context.Context, cluster, apiserver string) (kubernetes.Interface, error)

type StackConfig struct {
	Stack Stack

	Admin AdminClient

	StateDir string

	StoreEndpoint func(cluster string) (string, error)

	VaultStoreDSN func(cluster string) (string, error)

	AdvertiseAddress string

	BindAddress string

	MarketDialled bool

	VaultTLS func(dataDir string, hosts ...string) (VaultTLS, error)

	TLSSANs []string

	ServiceAccountIssuer string
	APIAudiences         []string

	CloudControllerManagerExec []string

	ExternalSecrets *ExternalSecrets

	AuditPolicyPath string

	Harvest func(ctx context.Context, cluster string) error

	GuardRides bool

	Links *Links
}

func StepsFor(cfg StackConfig) materialiser.StepBuilder {
	return func(o materialiser.Observed, l materialiser.Leases) ([]procgroup.Step, error) {
		cluster := o.Cluster.Name
		if cluster == "" {
			return nil, errors.New("clusterprovider: StepsFor was given an observation with no cluster name")
		}
		dataDir, err := ClusterDataDir(cfg.StateDir, cluster)
		if err != nil {
			return nil, err
		}
		advPort, k3sSecure, err := K3sAPIPorts(l, cluster)
		if err != nil {
			return nil, err
		}
		k3sControllerManager, err := leasePort(l, portallocator.PurposeK3sControllerManager, cluster)
		if err != nil {
			return nil, err
		}
		k3sScheduler, err := leasePort(l, portallocator.PurposeK3sScheduler, cluster)
		if err != nil {
			return nil, err
		}
		k3sCloudControllerManager, err := leasePort(l, portallocator.PurposeK3sCloudControllerManager, cluster)
		if err != nil {
			return nil, err
		}
		kineLease, ok := l[portallocator.PurposeKine]
		if !ok {
			return nil, fmt.Errorf("%w: kine (cluster %s)", ErrNoLease, cluster)
		}
		kineListen := fmt.Sprintf("127.0.0.1:%d", kineLease.Port)

		if cfg.BindAddress == "" {
			return nil, fmt.Errorf("%w (cluster %s)", clusterstack.ErrMissingBindAddress, cluster)
		}
		if cfg.StoreEndpoint == nil {
			return nil, fmt.Errorf("%w (cluster %s)", ErrNoStore, cluster)
		}
		store, err := cfg.StoreEndpoint(cluster)
		if err != nil {
			return nil, err
		}
		if store == "" {
			return nil, fmt.Errorf("%w (cluster %s)", ErrNoStore, cluster)
		}

		advertise, sans := cfg.announced()

		spec := clusterstack.Spec{
			DataDir:                       dataDir,
			Cluster:                       cluster,
			OpenBaoConfig:                 clusterstack.OpenBaoConfigPath(dataDir),
			StoreEndpoint:                 store,
			KineListen:                    kineListen,
			DatastoreEndpoint:             DaemonURL(kineListen),
			AdvertiseAddress:              advertise,
			BindAddress:                   cfg.BindAddress,
			MarketDialled:                 cfg.MarketDialled,
			TLSSANs:                       sans,
			AdvertisePort:                 advPort,
			K3sSecurePort:                 k3sSecure,
			K3sControllerManagerPort:      k3sControllerManager,
			K3sSchedulerPort:              k3sScheduler,
			K3sCloudControllerManagerPort: k3sCloudControllerManager,
			TokenFile:                     TokenPath(dataDir),
			CloudControllerManagerExec:    cfg.CloudControllerManagerExec,
			ServiceAccountIssuer:          cfg.ServiceAccountIssuer,
			APIAudiences:                  cfg.APIAudiences,
			Audit: clusterstack.Audit{
				PolicyPath: cfg.AuditPolicyPath,
				LogPath:    filepath.Join(dataDir, "audit.log"),
			},
		}

		if cfg.runsExternalSecrets() {
			spec.ExternalSecrets, err = externalSecretsSpec(cfg, l, cluster)
			if err != nil {
				return nil, err
			}
		}

		if cfg.Stack.Has(procgroup.ComponentOpenBao) {
			baoPort, err := leasePort(l, portallocator.PurposeOpenBao, cluster)
			if err != nil {
				return nil, err
			}
			spec.OpenBaoPort = baoPort
			if cfg.VaultTLS == nil {
				return nil, fmt.Errorf("%w (cluster %s)", ErrNoVaultTLS, cluster)
			}
			vaultTLS, err := cfg.VaultTLS(dataDir, advertise, "127.0.0.1")
			if err != nil {
				return nil, err
			}
			spec.OpenBaoTLSCertFile = vaultTLS.CertFile
			spec.OpenBaoTLSKeyFile = vaultTLS.KeyFile
			if cfg.VaultStoreDSN == nil {
				return nil, fmt.Errorf("%w (cluster %s)", ErrNoVaultStore, cluster)
			}
			dsn, err := cfg.VaultStoreDSN(cluster)
			if err != nil {
				return nil, err
			}
			if dsn == "" {
				return nil, fmt.Errorf("%w (cluster %s)", ErrNoVaultStore, cluster)
			}
			spec.OpenBaoPostgresDSN = dsn
			if err := WriteOpenBaoConfig(spec); err != nil {
				return nil, err
			}
		}

		if err := WriteKubeSchedulerConfig(spec); err != nil {
			return nil, err
		}

		steps, err := StepsFrom(spec, l, cfg.Stack)
		if err != nil {
			return nil, err
		}
		if cfg.GuardRides && !hasStepBeforeK3s(steps) {
			return nil, fmt.Errorf("%w (stack %v, cluster %s)", ErrNoGuardSeam, cfg.Stack, cluster)
		}
		if cfg.GuardRides {
			steps, err = HarvestAfterK3s(steps, cfg.Harvest, cluster)
			if err != nil {
				return nil, err
			}
		}
		steps, err = hardenAfterK3s(steps, cfg.Admin, cluster, APIServerURL(k3sSecure))
		if err != nil {
			return nil, err
		}
		if cfg.Stack.Has(procgroup.ComponentCloudControllerManager) {
			steps, err = prepareCloudControllerManager(steps, cfg, cluster, APIServerURL(k3sSecure))
			if err != nil {
				return nil, err
			}
		}
		if cfg.runsExternalSecrets() {
			steps, err = prepareExternalSecrets(steps, cfg, spec, cluster, APIServerURL(k3sSecure))
			if err != nil {
				return nil, err
			}
		}
		if cfg.Links != nil {
			steps, err = linksAfterK3s(steps, cfg, cluster, dataDir, l, k3sSecure, spec.OpenBaoPort)
			if err != nil {
				return nil, err
			}
		}
		return steps, nil
	}
}

func (cfg StackConfig) announced() (string, []string) {
	return cfg.AdvertiseAddress, []string{cfg.AdvertiseAddress}
}

func hasStepBeforeK3s(steps []procgroup.Step) bool {
	for i, s := range steps {
		if s.Name == procgroup.ComponentK3s {
			return i >= 1
		}
	}
	return false
}

func K3sAPIPorts(l materialiser.Leases, cluster string) (advertisePort, listenPort int, err error) {
	listenPort, err = leasePort(l, portallocator.PurposeK3sSecure, cluster)
	if err != nil {
		return 0, 0, err
	}
	advertisePort = listenPort
	return advertisePort, listenPort, nil
}

func leasePort(l materialiser.Leases, p portallocator.Purpose, cluster string) (int, error) {
	lease, ok := l[p]
	if !ok {
		return 0, fmt.Errorf("%w: %s (cluster %s)", ErrNoLease, p, cluster)
	}
	return lease.Port, nil
}

func WriteOpenBaoConfig(spec clusterstack.Spec) error {
	body, err := spec.OpenBaoServerConfig()
	if err != nil {
		return err
	}
	dir := filepath.Dir(spec.OpenBaoConfig)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: the directory %s holding %s: %v", ErrOpenBaoConfigUnwritable,
			dir, spec.OpenBaoConfig, err)
	}

	if info, err := os.Stat(spec.OpenBaoConfig); err == nil {
		current, err := os.ReadFile(spec.OpenBaoConfig)
		if err != nil {
			return fmt.Errorf("%w: reading %s: %v", ErrOpenBaoConfigUnwritable, spec.OpenBaoConfig, err)
		}
		if string(current) == body && info.Mode().Perm() == openBaoConfigMode {
			return nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: stat %s: %v", ErrOpenBaoConfigUnwritable, spec.OpenBaoConfig, err)
	}

	tmp, err := os.CreateTemp(dir, clusterstack.OpenBaoConfigFileName+".")
	if err != nil {
		return fmt.Errorf("%w: a temporary file beside %s: %v", ErrOpenBaoConfigUnwritable,
			spec.OpenBaoConfig, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(openBaoConfigMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: the mode of %s: %v", ErrOpenBaoConfigUnwritable, tmp.Name(), err)
	}
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: writing %s: %v", ErrOpenBaoConfigUnwritable, tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: closing %s: %v", ErrOpenBaoConfigUnwritable, tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), spec.OpenBaoConfig); err != nil {
		return fmt.Errorf("%w: renaming %s over %s: %v", ErrOpenBaoConfigUnwritable, tmp.Name(),
			spec.OpenBaoConfig, err)
	}
	return nil
}
