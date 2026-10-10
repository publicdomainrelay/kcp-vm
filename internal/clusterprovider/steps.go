package clusterprovider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const GateTimeout = 90 * time.Second

const PollInterval = 250 * time.Millisecond

const (
	EnvCloudControllerManagerKubeconfig = "SOCIALWEB_CLOUDCONTROLLERMANAGER_TENANT_KUBECONFIG"

	EnvCloudControllerManagerCluster = "SOCIALWEB_CLOUDCONTROLLERMANAGER_CLUSTER"

	EnvCloudControllerManagerInstances = "SOCIALWEB_CLOUDCONTROLLERMANAGER_INSTANCES_FILE"
)

const cloudControllerManagerReadyMark = `"phase":"Ready"`

var ErrNoLease = errors.New("clusterprovider: the cluster holds no lease for this component")

var ErrNoVaultStore = errors.New(
	"clusterprovider: the vault is in this Stack and no per-cluster postgres DSN was supplied " +
		"for it. The vault's store IS the cluster's own embedded-postgres, so without the DSN " +
		"the vault cannot be rendered and the Stack would lease a vault port and start nothing")

type Stack []procgroup.Component

var DesignedStack = Stack{procgroup.ComponentOpenBao, procgroup.ComponentKine, procgroup.ComponentK3s}

var ProvenStack = Stack{procgroup.ComponentK3s}

func (s Stack) Has(c procgroup.Component) bool {
	for _, have := range s {
		if have == c {
			return true
		}
	}
	return false
}

func (s Stack) NeedsLocalPostgres(localStore bool) bool {
	return localStore || s.Has(procgroup.ComponentOpenBao)
}

func componentLog(dataDir string, c procgroup.Component) (*os.File, error) {
	if err := os.MkdirAll(LogDir(dataDir), 0o700); err != nil {
		return nil, fmt.Errorf("clusterprovider: the log directory for this cluster's components: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(LogDir(dataDir), string(c)+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: %s's log. A component whose output goes nowhere "+
			"cannot be diagnosed: the readiness gate reports a dial that never answered, and the "+
			"reason it never answered is in this file: %w", c, err)
	}
	return f, nil
}

func LogDir(dataDir string) string { return filepath.Join(dataDir, "logs") }

func StepsFrom(spec clusterstack.Spec, leases materialiser.Leases, stack Stack) ([]procgroup.Step, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if len(stack) == 0 {
		return nil, errors.New("clusterprovider: the Stack is empty; a group with no components is not a cluster")
	}
	in := make(map[procgroup.Component]bool, len(stack))
	for _, c := range stack {
		if c == procgroup.ComponentPostgres {
			return nil, errors.New(
				"clusterprovider: ComponentPostgres cannot be a Step (clusterstack.ErrNotAProcess): " +
					"embedded-postgres is a library and is started by the component that links it")
		}
		in[c] = true
	}

	var bao portallocator.Lease
	if in[procgroup.ComponentOpenBao] {
		var ok bool
		if bao, ok = leases[portallocator.PurposeOpenBao]; !ok {
			return nil, fmt.Errorf("%w: openbao", ErrNoLease)
		}
	}

	api := leases[portallocator.PurposeK3sSecure]

	steps := make([]procgroup.Step, 0, len(stack))
	for _, c := range procgroup.StackOrder {
		if !in[c] {
			continue
		}
		argv, err := spec.Argv(c)
		if err != nil {
			return nil, err
		}
		if len(argv) == 0 {
			return nil, fmt.Errorf("clusterprovider: %s has an empty argv", c)
		}
		log, err := componentLog(spec.DataDir, c)
		if err != nil {
			return nil, err
		}
		steps = append(steps, procgroup.Step{
			Name:  c,
			Spec:  processrunner.Spec{Argv: argv, Env: stepEnv(spec, c), Output: log},
			Ready: gate(c, spec, bao, api),
		})
	}
	return steps, nil
}

func stepEnv(spec clusterstack.Spec, c procgroup.Component) []string {
	if c == procgroup.ComponentExternalSecrets || c == procgroup.ComponentExternalSecretsWebhook {
		return externalSecretsEnv(spec)
	}
	if c != procgroup.ComponentCloudControllerManager {
		return nil
	}
	return []string{
		EnvCloudControllerManagerKubeconfig + "=" + clusterstack.CloudControllerManagerKubeconfig(spec.DataDir),
		EnvCloudControllerManagerCluster + "=" + spec.Cluster,
		EnvCloudControllerManagerInstances + "=" + clusterstack.NodeSnapshotPath(spec.DataDir),
	}
}

func gate(c procgroup.Component, spec clusterstack.Spec, bao, api portallocator.Lease) func(context.Context, *processrunner.Process) error {
	return func(ctx context.Context, p *processrunner.Process) error {
		ctx, cancel := context.WithTimeout(ctx, GateTimeout)
		defer cancel()
		switch c {
		case procgroup.ComponentOpenBao:
			seal, err := EnsureVault(ctx, spec.DataDir, bao.Port)
			if err != nil {
				return err
			}
			if err := ApplyClusterVault(ctx, spec.DataDir, seal); err != nil {
				return err
			}
			return waitOpenBao(ctx, spec.DataDir, bao.Port)
		case procgroup.ComponentKine:
			return waitDial(ctx, spec.KineListen)
		case procgroup.ComponentK3s:
			if api.Purpose != portallocator.PurposeK3sSecure {
				return fmt.Errorf("%w: the apiserver, so this k3s step has no leased port to hold it to (data dir %s)", ErrNoLease, spec.DataDir)
			}
			if err := portallocator.AssertMarketAdvertiseInvariant(spec.AdvertiseAddress, spec.AdvertisePort, spec.AdvertisePort, api); err != nil {
				return err
			}
			if err := waitK3s(ctx, spec.BindAddress, spec.K3sSecurePort); err != nil {
				return err
			}
			return waitK3sControllers(ctx, spec.DataDir)
		case procgroup.ComponentCloudControllerManager:
			return waitCloudControllerManager(ctx, p)
		case procgroup.ComponentExternalSecretsWebhook:
			return waitExternalSecretsWebhook(ctx, spec, p)
		case procgroup.ComponentExternalSecrets:
			return waitExternalSecretsController(ctx, spec, p)
		}
		return fmt.Errorf("clusterprovider: no readiness gate for %q", c)
	}
}

type errNotReady struct {
	what string
	last string
}

func (e *errNotReady) Error() string {
	return fmt.Sprintf("%s never became ready (last observation: %s)", e.what, e.last)
}

func waitOpenBao(ctx context.Context, dataDir string, port int) error {
	pool, err := vaultCA(dataDir)
	if err != nil {
		return err
	}
	return waitOpenBaoTLS(ctx, VaultAddr(port), pool)
}

func waitOpenBaoTLS(ctx context.Context, addr string, pool *x509.CertPool) error {
	url := addr + "/v1/sys/health"
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	last := "no answer"
	return poll(ctx, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK, http.StatusTooManyRequests, 473:
			return true, nil
		}
		last = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return false, nil
	}, func() error { return &errNotReady{what: "openbao", last: last} })
}

func waitDial(ctx context.Context, addr string) error {
	last := "no answer"
	return poll(ctx, func(context.Context) (bool, error) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		_ = conn.Close()
		return true, nil
	}, func() error { return &errNotReady{what: "kine at " + addr, last: last} })
}

func waitK3s(ctx context.Context, host string, port int) error {
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	url := fmt.Sprintf("https://%s:%d/readyz", host, port)
	last := "no answer"
	return poll(ctx, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		defer resp.Body.Close()
		if apiserverIsAnswering(resp.StatusCode) {
			return true, nil
		}
		last = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return false, nil
	}, func() error { return &errNotReady{what: "k3s server at " + url, last: last} })
}

func waitK3sControllers(ctx context.Context, dataDir string) error {
	kubeconfig := clusterstack.AdminKubeconfig(dataDir)
	last := "no answer"
	return poll(ctx, func(ctx context.Context) (bool, error) {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		core, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		addons, err := core.AppsV1().Deployments(metav1.NamespaceSystem).List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			last = err.Error()
			return false, nil
		}
		if len(addons.Items) == 0 {
			last = "the apiserver answered and " + metav1.NamespaceSystem + " holds no Deployment yet"
			return false, nil
		}
		return true, nil
	}, func() error {
		return &errNotReady{
			what: "the k3s controllers of the server whose admin kubeconfig is " + kubeconfig +
				". The apiserver answers /readyz before they start, and k3s registers the " +
				"node-password validator that answers /v1-k3s/*.crt for a REMOTE node on the line " +
				"before it sets Runtime.Core: a node given its guest in that window asks for its " +
				"serving certificate, is refused with 503 Service Unavailable, and retries on a " +
				"backoff of five seconds jittered by the same five, which is the difference between " +
				"a node that joins at five seconds and one that joins at twelve to twenty. The " +
				"controllers' own first write is an addon Deployment in kube-system and that is what " +
				"this waits for: /v1-k3s/readyz is the endpoint that flips exactly with them, and it " +
				"takes a node's credentials, which this process does not hold",
			last: last,
		}
	})
}

func waitCloudControllerManager(ctx context.Context, p *processrunner.Process) error {
	last := "no output yet"
	return poll(ctx, func(context.Context) (bool, error) {
		out := p.Output()
		if strings.Contains(out, cloudControllerManagerReadyMark) {
			return true, nil
		}
		last = fmt.Sprintf("the process has written %d bytes and no readiness line", len(out))
		return false, nil
	}, func() error {
		return &errNotReady{
			what: "the cloud controller manager's informer sync. It logs phase Ready once its " +
				"Node informer has synced, which is the first moment it can act; before that it has " +
				"observed nothing and would write nothing. It listens on no port, so this line is the " +
				"only readiness signal there is",
			last: last,
		}
	})
}

func apiserverIsAnswering(code int) bool {
	switch code {
	case http.StatusOK, http.StatusUnauthorized:
		return true
	default:
		return false
	}
}

func poll(ctx context.Context, up func(context.Context) (bool, error), timeout func() error) error {
	for {
		ok, err := up(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return timeout()
		case <-time.After(PollInterval):
		}
	}
}

func Probe(ctx context.Context, url string) (code int, reachable bool) {
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/readyz", nil)
	if err != nil {
		return 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	code = resp.StatusCode
	return code, apiserverIsAnswering(code)
}
