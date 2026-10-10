package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/cloudprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/instance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scaledriver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

const (
	fourstepImageStoreEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_IMAGE_OUT_DIR"

	fourstepImageConfigEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_IMAGE_CONFIG"

	fourstepQEMUImageConfigEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_QEMU_IMAGE_CONFIG"

	fourstepSubstrateEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_SUBSTRATE"

	fourstepFirecrackerEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_FIRECRACKER"

	fourstepQEMUEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_QEMU"

	fourstepAgentEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_NODE_AGENT"

	fourstepK3sEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_K3S"

	fourstepKubectlEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_KUBECTL"

	fourstepTenantImageEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_TENANT_IMAGE"

	fourstepHoldEnv = "SOCIALWEB_CLUSTERPROVIDER_LIVE_SCALE_HOLD"

	fourstepDefaultHold = "1m"

	fourstepIdleWindow = 20 * time.Second

	fourstepIdlePoll = 2 * time.Second

	fourstepReadyPoll = 250 * time.Millisecond

	fourstepCluster = "liveweb"

	fourstepAccount = "did:plc:fourstep000000000000000"

	fourstepDeadline = 20 * time.Minute
)

type fourstepBins struct {
	substrate string

	vmmBin string

	imageConfig string

	k3s string

	kubectl string

	tenantImage string

	cloudControllerManager string
}

type fourstepStack struct {
	cfg Config

	bins fourstepBins

	alloc *portallocator.Allocator

	prov *clusterprovider.Provider

	driver *scaledriver.Driver

	registry *fourstepClusters

	tenant tenantClient
}

func fourstepRequirements(t *testing.T, substrate string) fourstepBins {
	t.Helper()
	livegate.Short(t, "this test runs the four-step proof through the composition root over real components and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	imageStore := os.Getenv(fourstepImageStoreEnv)
	if imageStore == "" {
		livegate.Require(t, "set %s, %s, %s (default %s), the VMM binary for that substrate (%s for "+
			"firecracker, %s for qemu), %s, %s, %s and %s to the store socialweb-nodeimage published "+
			"into, the builder's own configuration -- which names the image in that store, so it "+
			"has to be the configuration the build read and its inputs have to be the files the "+
			"build read -- the agent built for the guest, k3s, kubectl and an "+
			"image the guest already carries and stays up on with no arguments, and this test "+
			"derives the whole objective on this host: "+
			"a control plane at Ready with zero nodes, `get nodes` empty, an empty cluster that "+
			"STAYS at zero, a node provisioned by the scale decision itself when a tenant workload "+
			"appears, and the cluster back at zero with the control plane still answering once that "+
			"workload is gone. It is skipped by default because it boots a real guest, and a skip "+
			"here is NOT a pass: nothing else in this tree reaches newNodeHosts, the substrate "+
			"packages and tenantNodes together. HOW FAR IT HAS BEEN RUN: through to the end as a Go "+
			"test on 2026-09-23 for firecracker, one uninterrupted run, nothing deleted by hand, "+
			"99.5s wall, against a node image built from the 48-module list -- "+
			"INSTANCE_START_TO_READY_S=9.749 measured on the host from firecracker's InstanceStart "+
			"to the Node's Ready condition; the qemu run is recorded in docs/ARCHITECTURE.md. The "+
			"objective was first measured through the composition root by a scripted harness, one "+
			"uninterrupted run, nothing deleted by hand: an EMPTY tenant cluster sat "+
			"at zero and stayed there while k3s's own coredns, metrics-server and "+
			"local-path-provisioner were Pending; a tenant workload applied at zero nodes gave "+
			"\"Provision, nodes=1\"; the guest joined as a Ready node (INSTANCE_START_TO_READY_S="+
			"9.609 measured on the host from firecracker's InstanceStart) and its pod ran on it; "+
			"deleting ONLY the tenant workload gave \"Teardown, nodes=0, nothing running for "+
			"3m0.002514861s, past the 3m0s hold\" while the control plane's own Deployments were "+
			"still Running; `get nodes` was then empty and /readyz still answered. What this test "+
			"adds over that harness is the regression guard: the same four transitions, derived "+
			"from scratch, through the substrate the configuration names",
			fourstepImageStoreEnv, fourstepImageConfigEnv, fourstepSubstrateEnv,
			SubstrateFirecracker, fourstepFirecrackerEnv, fourstepQEMUEnv, fourstepAgentEnv,
			fourstepK3sEnv, fourstepKubectlEnv, fourstepTenantImageEnv)
	}
	vmmEnv := fourstepFirecrackerEnv
	imageConfigEnv := fourstepImageConfigEnv
	imageConfig := strings.TrimSpace(os.Getenv(fourstepImageConfigEnv))
	if substrate == SubstrateQEMU {
		vmmEnv = fourstepQEMUEnv
		if q := strings.TrimSpace(os.Getenv(fourstepQEMUImageConfigEnv)); q != "" {
			imageConfigEnv = fourstepQEMUImageConfigEnv
			imageConfig = q
		}
	}
	bins := fourstepBins{
		substrate:   substrate,
		vmmBin:      os.Getenv(vmmEnv),
		imageConfig: imageConfig,
		k3s:         os.Getenv(fourstepK3sEnv),
		kubectl:     os.Getenv(fourstepKubectlEnv),
		tenantImage: os.Getenv(fourstepTenantImageEnv),
	}
	bins.cloudControllerManager = fourstepBuildCloudControllerManager(t)
	for _, req := range []struct{ name, value string }{
		{imageConfigEnv, bins.imageConfig},
		{vmmEnv, bins.vmmBin},
		{fourstepAgentEnv, os.Getenv(fourstepAgentEnv)},
		{fourstepK3sEnv, bins.k3s},
		{fourstepKubectlEnv, bins.kubectl},
		{fourstepTenantImageEnv, bins.tenantImage},
	} {
		if req.value == "" {
			t.Fatalf("%s is not set", req.name)
		}
	}
	for _, bin := range []string{bins.k3s, bins.kubectl} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s is not runnable: %v", bin, err)
		}
	}
	fourstepSkipWithoutPostgres(t)
	for _, bin := range []string{"k3s", "kine"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH: the four-step proof runs the real components, and a skip "+
				"here is NOT a pass -- nothing else in this tree starts a process group at all", bin)
		}
	}
	if err := fourstepNodePasswordWritable(); err != nil {
		livegate.RequireLocal(t, "k3s server --disable-agent is not a server without an agent: it still bootstraps "+
			"the host-global node password, and this process cannot open %s: %v. MEASURED: the "+
			"server then logs \"Waiting to retrieve agent configuration; server is not ready: open "+
			"/etc/rancher/node/password: permission denied\" for ever and /readyz never leaves 503. "+
			"k3s's path is NOT under --data-dir and no flag moves it, so the gateway's user must "+
			"OWN it -- and postgres must not run as root, so neither `as root` nor `as a plain "+
			"user` is the whole story. Override with SOCIALWEB_K3S_NODE_PASSWORD if this host "+
			"keeps it elsewhere", fourstepNodePasswordPath(), err)
	}
	return bins
}

func fourstepBuildCloudControllerManager(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "socialweb-cloud-controller-manager")
	cmd := exec.Command("go", "build", "-o", bin, "../socialweb-cloud-controller-manager")
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("building the cloud controller manager this proof installs into the process "+
			"group: %v\n%s. The proof builds the binary rather than taking a pre-built one, so its "+
			"result names the tree it ran against", err, out.String())
	}
	return bin
}

func fourstepNodePasswordPath() string {
	if p := os.Getenv("SOCIALWEB_K3S_NODE_PASSWORD"); p != "" {
		return p
	}
	return "/etc/rancher/node/password"
}

func fourstepNodePasswordWritable() error {
	f, err := os.OpenFile(fourstepNodePasswordPath(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

func fourstepSkipWithoutPostgres(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts the cluster's own real postgres and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	for _, bin := range []string{"initdb", "postgres", "psql"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH: the cluster's own store is a postgres this gateway starts, "+
				"and the store IS the cluster, so there is nothing to exercise", bin)
		}
	}
}

func fourstepHold() string {
	if raw := strings.TrimSpace(os.Getenv(fourstepHoldEnv)); raw != "" {
		return raw
	}
	return fourstepDefaultHold
}

func fourstepConfig(t *testing.T, bins fourstepBins) Config {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	stateDir, err := os.MkdirTemp("", "kcp-fourstep-")
	if err != nil {
		t.Fatalf("creating the proof's state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	mintDir := filepath.Join(stateDir, "mint")
	if err := os.MkdirAll(mintDir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", mintDir, err)
	}
	slot, err := liveports.TakeForProcess(liveports.FourStepProof)
	if err != nil {
		t.Fatalf("%v: this proof runs the composition root, which binds a store, an apiserver and "+
			"three control-plane ports on this host", err)
	}
	env := map[string]string{
		"SOCIALWEB_KCP_HOST":                                 "https://127.0.0.1:6443",
		"SOCIALWEB_KCP_KUBECONFIG":                           filepath.Join(stateDir, "kcp.yaml"),
		"SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH":               filepath.Join(stateDir, "leases.json"),
		"SOCIALWEB_CLUSTERPROVIDER_JOIN":                     fourstepCluster,
		"SOCIALWEB_GATEWAY_MINT_KUBECONFIGS":                 mintDir,
		"SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW":        "false",
		"SOCIALWEB_CLUSTERPROVIDER_STATE_DIR":                stateDir,
		"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE":                bins.substrate,
		"SOCIALWEB_CLUSTERPROVIDER_HOST":                     host,
		"SOCIALWEB_CLUSTERPROVIDER_NETWORK":                  NetworkPasta,
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR":            os.Getenv(fourstepImageStoreEnv),
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG":             bins.imageConfig,
		"SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT":               os.Getenv(fourstepAgentEnv),
		"SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER": bins.cloudControllerManager,
		"SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE":               liveports.Text(slot),
		"SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD":               fourstepHold(),
		"SOCIALWEB_CLUSTERPROVIDER_NODE_DISK_MIB":            os.Getenv(envNodeDiskMiB),
		"SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES":          os.Getenv(envScaleMaxNodes),
		"SOCIALWEB_CLUSTERPROVIDER_SCALE_PODS_PER_NODE":      os.Getenv(envScalePodsPerNode),
	}
	if bins.substrate == SubstrateQEMU {
		env["SOCIALWEB_CLUSTERPROVIDER_QEMU"] = bins.vmmBin
	} else {
		env["SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER"] = bins.vmmBin
	}
	cfg, err := load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("the placement this test hands to the composition root was refused: %v. It is "+
			"built as an environment IMAGE the test supplies rather than one read from this "+
			"process's environment, so the refusal is about the placement and not about the test "+
			"runner's own shell", err)
	}
	return cfg
}

type fourstepClusters struct {
	cluster *v1alpha1.Cluster

	writes []v1alpha1.ClusterStatus

	nodes []v1alpha1.NodePhase

	finalized bool

	live func() bool
}

func (p *fourstepClusters) Refs(context.Context) ([]clusterprovider.Ref, error) {
	return []clusterprovider.Ref{{
		Account: "root:fourstep",
		DID:     p.cluster.Spec.Account,
		Home:    "root:fourstep:home",
		Name:    p.cluster.Name,
	}}, nil
}

func (p *fourstepClusters) Read(context.Context, clusterprovider.Ref) (*v1alpha1.Cluster, error) {
	c := *p.cluster
	return &c, nil
}

func (p *fourstepClusters) RemoveFinalizer(_ context.Context, _ clusterprovider.Ref) error {
	p.finalized = true
	return nil
}

func (p *fourstepClusters) WriteStatus(_ context.Context, _ clusterprovider.Ref, st v1alpha1.ClusterStatus) error {
	p.writes = append(p.writes, st)
	p.cluster.Status = st
	return nil
}

func (p *fourstepClusters) MountStatus(context.Context, clusterprovider.Ref) int {
	if p.cluster.DeletionTimestamp != nil {
		return 503
	} else if p.live != nil && p.live() {
		return 401
	} else {
		return 502
	}
}

func (p *fourstepClusters) IdentityState(context.Context, clusterprovider.Ref) (v1alpha1.IdentityState, error) {
	return v1alpha1.IdentityRevoked, nil
}

func (p *fourstepClusters) Nodes(context.Context, clusterprovider.Ref) ([]v1alpha1.Node, error) {
	return nil, nil
}

func (p *fourstepClusters) WriteNode(_ context.Context, _ clusterprovider.Ref, _ string, status v1alpha1.NodeStatus) error {
	p.nodes = append(p.nodes, status.Phase)
	return nil
}

func fourstepStore(pg *clusterprovider.Postgres) func(string) (string, error) {
	return func(string) (string, error) { return pg.DSN(), nil }
}

func fourstepGuard(t *testing.T, stateDir, cluster string, pg *clusterprovider.Postgres, log *slog.Logger) clusterprovider.GuardDeps {
	t.Helper()
	tokens := map[string]instance.Token{}
	return clusterprovider.GuardDeps{
		StateDir: stateDir,
		Token: func(name string) instance.Token {
			if tok, ok := tokens[name]; ok {
				return tok
			}
			tok, err := instance.New(nil)
			if err != nil {
				t.Fatalf("instance.New: %v", err)
			}
			dir, err := clusterprovider.ClusterDataDir(stateDir, name)
			if err != nil {
				t.Fatalf("clusterprovider.ClusterDataDir: %v", err)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("creating %s: %v", dir, err)
			}
			if err := instance.WriteFile(clusterprovider.TokenPath(dir), tok); err != nil {
				t.Fatalf("writing the token: %v", err)
			}
			tokens[name] = tok
			return tok
		},
		HasRow: func(string) bool {
			has, err := pg.HasBootstrapRow(context.Background())
			if err != nil {
				t.Fatalf("HasBootstrapRow: %v", err)
			}
			return has
		},
		DeleteRow: func(ctx context.Context, name string) error {
			return pg.DeleteBootstrapRows(ctx)
		},
		SeedBundle: func(_ context.Context, name string) error {
			return clusterprovider.SeedBundle(stateDir, name)
		},
		Bundle: clusterprovider.BundleGuard(stateDir),
		Log:    log,
	}
}

func fourstepBringUp(t *testing.T, ctx context.Context, cfg Config, bins fourstepBins, log *slog.Logger) *fourstepStack {
	t.Helper()
	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: cfg.LeasePath,
		Range:     cfg.PortRange,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}

	nets, err := subnetallocator.Open(subnetallocator.Config{
		LeasePath: subnetLeasePath(cfg.StateDir),
	})
	if err != nil {
		t.Fatalf("subnetallocator.Open: %v", err)
	}

	pgLease, err := alloc.Allocate(portallocator.Request{
		Cluster: fourstepCluster, Purpose: portallocator.PurposePostgres,
	})
	if err != nil {
		t.Fatalf("the store's lease: %v", err)
	}
	pg, err := clusterprovider.StartPostgres(ctx, clusterprovider.PostgresSpec{
		Cluster:  fourstepCluster,
		StateDir: filepath.Join(cfg.StateDir, "store"),
		Port:     pgLease.Port,
		BinDir:   cfg.PostgresBinDir,
		Log:      log,
	})
	if err != nil {
		t.Fatalf("clusterprovider.StartPostgres: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := pg.Stop(stopCtx); err != nil {
			t.Logf("stopping the store: %v", err)
		}
	})

	policy := filepath.Join(t.TempDir(), "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages:\n  - RequestReceived\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatalf("writing the audit policy: %v", err)
	}

	stackCfg := clusterprovider.StackConfig{
		Stack: clusterprovider.Stack{
			procgroup.ComponentKine, procgroup.ComponentK3s, procgroup.ComponentCloudControllerManager,
		},
		Admin:                  adminForCluster(cfg.StateDir),
		StateDir:               cfg.StateDir,
		StoreEndpoint:          fourstepStore(pg),
		GuestDialled:           true,
		GuestGateway:           guestGateway(nets),
		BindAddress:            portallocator.LoopbackAdvertiseAddress,
		AuditPolicyPath:        policy,
		CloudControllerManager: bins.cloudControllerManager,
		Harvest: func(_ context.Context, cluster string) error {
			return clusterprovider.HarvestBundle(cfg.StateDir, cluster)
		},
		GuardRides: true,
	}
	purposes, err := clusterprovider.PurposesFor(stackCfg.Stack, true)
	if err != nil {
		t.Fatalf("clusterprovider.PurposesFor: %v", err)
	}

	host := materialiser.NewLocalHost(procgroup.Options{})
	host.PostgresUp = func(string) bool { return pg.Up() }
	t.Cleanup(func() {
		reapCtx, reap := context.WithTimeout(context.Background(), 60*time.Second)
		defer reap()
		if err := host.Reap(reapCtx, fourstepCluster); err != nil {
			t.Logf("reaping the control plane's process group: %v", err)
		}
	})

	decider, err := materialiser.New(materialiser.Options{
		Host:        host,
		Ports:       alloc,
		Networks:    nets,
		Steps:       clusterprovider.StepsFor(stackCfg),
		Identity:    clusterprovider.GuardFor(fourstepGuard(t, cfg.StateDir, fourstepCluster, pg, log)),
		NewInstance: func() string { return "gen-fourstep-1" },
		Purposes:    purposes,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}

	registry := &fourstepClusters{cluster: &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: fourstepCluster},
		Spec:       v1alpha1.ClusterSpec{Account: fourstepAccount, Running: true},
	}, live: func() bool { return host.Live(fourstepCluster) }}
	prov, err := clusterprovider.New(clusterprovider.Options{
		Registry:         registry,
		Decider:          decider,
		Ports:            alloc,
		Host:             host,
		WriteStatus:      true,
		NodeExpirySource: clusterprovider.NodeExpiryRollup,
		Interval:         time.Second,
		Log:              log,
	})
	if err != nil {
		t.Fatalf("clusterprovider.New: %v", err)
	}

	tenant := tenantClients(registry, tenantFromClusterCA(alloc, cfg.StateDir))
	substrate, err := nodeSubstrateFor(cfg)
	if err != nil {
		t.Fatalf("the substrate the configuration names: %v", err)
	}
	nodes, err := newNodeHosts(nodeHostsOptions{
		Allocator:        alloc,
		Networks:         nets,
		Registry:         registry,
		StateDir:         cfg.StateDir,
		AdvertiseAddress: cfg.AdvertiseAddress,
		Tenant:           tenant,
		Log:              log,
		Substrate:        substrate,
	})
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	t.Cleanup(func() {
		reapCtx, reap := context.WithTimeout(context.Background(), 2*time.Minute)
		defer reap()
		if err := nodes.reap(reapCtx); err != nil {
			t.Logf("reaping the node hosts: %v", err)
		}
	})

	driver, err := scaledriver.New(nodes.forCluster, scaledriver.Options{
		Observe: clusterprovider.NodeObserver{Client: tenant}.Observe,
		Scale: scale.Options{
			Hold:        cfg.ScaleHold,
			MaxNodes:    cfg.ScaleMaxNodes,
			PodsPerNode: cfg.ScalePodsPerNode,
		},
		Log: log,
	})
	if err != nil {
		t.Fatalf("scaledriver.New: %v", err)
	}

	return &fourstepStack{
		cfg: cfg, bins: bins, alloc: alloc, prov: prov, driver: driver,
		registry: registry, tenant: tenant,
	}
}

func (s *fourstepStack) ref() clusterprovider.Ref {
	return clusterprovider.Ref{
		Account: "root:fourstep",
		DID:     fourstepAccount,
		Home:    "root:fourstep:home",
		Name:    fourstepCluster,
	}
}

func (s *fourstepStack) tenantClient() (kubernetes.Interface, error) {
	return s.tenant(context.Background(), fourstepCluster)
}

func (s *fourstepStack) kubeconfig(t *testing.T) string {
	t.Helper()
	lease, ok := s.alloc.PortFor(fourstepCluster, portallocator.PurposeK3sSecure)
	if !ok {
		t.Fatalf("no apiserver lease for %s", fourstepCluster)
	}
	minted, err := clusterprovider.MintTenantCredentialForCluster(
		context.Background(), s.cfg.StateDir, fourstepCluster, clusterprovider.APIServerURL(lease.Port))
	if err != nil {
		t.Fatalf("minting the tenant credential: %v", err)
	}
	raw, err := clientcmd.Write(clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{"kcp": {
			Server:                   minted.Host,
			CertificateAuthorityData: minted.CAData,
			InsecureSkipTLSVerify:    minted.Insecure,
		}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"kcp": {
			ClientCertificateData: minted.CertData,
			ClientKeyData:         minted.KeyData,
			Token:                 minted.BearerToken,
		}},
		Contexts:       map[string]*clientcmdapi.Context{"kcp": {Cluster: "kcp", AuthInfo: "kcp"}},
		CurrentContext: "kcp",
	})
	if err != nil {
		t.Fatalf("encoding the tenant kubeconfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tenant.kubeconfig")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing the tenant kubeconfig: %v", err)
	}
	return path
}

func (s *fourstepStack) tryNodes(kubeconfig string) (string, error) {
	cmd := exec.Command(s.bins.kubectl, "--kubeconfig", kubeconfig,
		"--request-timeout=15s", "get", "nodes", "--no-headers")
	var listed, complained strings.Builder
	cmd.Stdout = &listed
	cmd.Stderr = &complained
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(listed.String() + " " + complained.String()), err
	}
	return strings.TrimSpace(listed.String()), nil
}

func (s *fourstepStack) pass(t *testing.T, ctx context.Context) scale.Decision {
	t.Helper()
	pass, err := s.prov.Reconcile(ctx, s.ref())
	if err != nil {
		t.Logf("the pass failed: %v", err)
	} else {
		t.Logf("pass: phase=%s ops=%v alert=%q", pass.Result.Phase, pass.Result.Ops, pass.Result.Alert)
	}
	dec, err := s.driver.RunPass(ctx, fourstepCluster)
	if err != nil {
		t.Logf("the node scale pass did not complete: %v", err)
	}
	return dec
}

func (s *fourstepStack) waitReady(t *testing.T, ctx context.Context) materialiser.Result {
	t.Helper()
	want := readyPhase(t)
	deadline := time.Now().Add(5 * time.Minute)
	for {
		pass, err := s.prov.Reconcile(ctx, s.ref())
		if err == nil && pass.Result.Phase == want {
			return pass.Result
		}
		if time.Now().After(deadline) {
			t.Fatalf("the control plane never reached %s; last status %+v", want, s.registry.cluster.Status)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *fourstepStack) waitNodes(t *testing.T, kubeconfig string, want int) string {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	last := ""
	for {
		got, err := s.tryNodes(kubeconfig)
		if err != nil {
			last = strings.TrimSpace(got + " " + err.Error())
		} else {
			last = got
			if countLines(got) == want {
				return got
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("`kubectl get nodes` did not report %d of them within 6m; last said %q. The "+
				"count is what the objective counts, and it is the number the scale decision acts on",
				want, last)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *fourstepStack) waitNodeReady(t *testing.T, ctx context.Context) {
	t.Helper()
	client, err := s.tenantClient()
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}
	name, err := nodeprovider.NodeName(fourstepCluster, nodeID)
	if err != nil {
		t.Fatalf("nodeprovider.NodeName: %v", err)
	}
	deadline := time.Now().Add(6 * time.Minute)
	for {
		node, err := client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node %s never reported Ready within 6m; the pod that is what the node "+
				"was provisioned for cannot run until it is", name)
		}
		time.Sleep(fourstepReadyPoll)
	}
}

func (s *fourstepStack) kubectlTemplate(kubeconfig, name, template string) (string, error) {
	cmd := exec.Command(s.bins.kubectl, "--kubeconfig", kubeconfig, "--request-timeout=15s",
		"get", "node", name, "-o", template)
	var out, complained strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &complained
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String() + " " + complained.String()), err
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *fourstepStack) assertCloudIdentity(t *testing.T, kubeconfig string) {
	t.Helper()
	name, err := nodeprovider.NodeName(fourstepCluster, nodeID)
	if err != nil {
		t.Fatalf("nodeprovider.NodeName: %v", err)
	}
	want := cloudprovider.ProviderID(fourstepCluster, name)
	label := `go-template={{ index .metadata.labels "` + cloudprovider.LabelCluster + `" }}`
	deadline := time.Now().Add(2 * time.Minute)
	last := ""
	for {
		providerID, pidErr := s.kubectlTemplate(kubeconfig, name, "jsonpath={.spec.providerID}")
		cluster, labelErr := s.kubectlTemplate(kubeconfig, name, label)
		if pidErr == nil && labelErr == nil && providerID == want && cluster == fourstepCluster {
			t.Logf("STEP 3 the cloud controller manager stamped the Node %s: providerID=%s %s=%s",
				name, providerID, cloudprovider.LabelCluster, cluster)
			return
		}
		last = fmt.Sprintf("providerID=%q (err %v), %s=%q (err %v)",
			providerID, pidErr, cloudprovider.LabelCluster, cluster, labelErr)
		if time.Now().After(deadline) {
			t.Fatalf("the Node %s never carried the cloud identity the controller stamps: want "+
				"providerID %q and %s=%q; last read %s. A controller that is installed and does "+
				"nothing reads green here and is the failure this assertion exists to catch",
				name, want, cloudprovider.LabelCluster, fourstepCluster, last)
		}
		time.Sleep(fourstepReadyPoll)
	}
}

func (s *fourstepStack) logInstanceStartToReady(t *testing.T, readyAt time.Time) {
	t.Helper()
	name, err := nodeprovider.NodeName(fourstepCluster, nodeID)
	if err != nil {
		t.Fatalf("nodeprovider.NodeName: %v", err)
	}
	dir := clusterprovider.NodeDataDir(s.cfg.StateDir, fourstepCluster, name)
	raw, err := os.ReadFile(nodevm.InstancePath(dir))
	if err != nil {
		t.Fatalf("reading the VMM's instance record at %s, which is what makes this measurement "+
			"host-timed rather than taken from the guest's own clock: %v", nodevm.InstancePath(dir), err)
	}
	var rec nodevm.InstanceRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("the instance record at %s: %v", nodevm.InstancePath(dir), err)
	}
	if rec.StartNanos <= 0 {
		t.Fatalf("the instance record at %s carries startNanos %d, so the VMM's start cannot be "+
			"placed on the host's clock", nodevm.InstancePath(dir), rec.StartNanos)
	}
	start := time.Unix(0, rec.StartNanos)
	t.Logf("INSTANCE_START_TO_READY_S=%.3f measured on the host from the %s VMM's instance "+
		"record to the Node's Ready condition, resolution %s",
		readyAt.Sub(start).Seconds(), s.bins.substrate, fourstepReadyPoll)
}

func (s *fourstepStack) choose(t *testing.T, ctx context.Context, want scale.Action) scale.Decision {
	t.Helper()
	deadline := time.Now().Add(8 * time.Minute)
	for {
		dec := s.pass(t, ctx)
		if dec.Action == want {
			t.Logf("the scale decision reads %s: %s", dec.Action, dec.Reason)
			return dec
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			t.Fatalf("the scale decision never read %s; last was %s (%s)", want, dec.Action, dec.Reason)
		}
		time.Sleep(time.Second)
	}
}

func (s *fourstepStack) applyWorkload(t *testing.T, ctx context.Context) {
	t.Helper()
	client, err := s.tenantClient()
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}
	one := int32(1)
	workload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "fourstep", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "fourstep"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "fourstep"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "fourstep",
						Image:           s.bins.tenantImage,
						ImagePullPolicy: corev1.PullIfNotPresent,
					}},
				},
			},
		},
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err = client.AppsV1().Deployments("default").Create(ctx, workload, metav1.CreateOptions{})
		if err == nil {
			return
		}
		if !apierrors.IsNotFound(err) && !strings.Contains(err.Error(), "serviceaccount") {
			t.Fatalf("applying the tenant workload at zero nodes: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the default namespace never admitted the tenant workload: %v. k3s creates "+
				"the namespace the moment the apiserver answers, and the ServiceAccount in it is "+
				"created by the controller-manager's first sync, so applying immediately races it", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (s *fourstepStack) waitWorkloadRunning(t *testing.T, ctx context.Context) {
	t.Helper()
	client, err := s.tenantClient()
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}
	deadline := time.Now().Add(6 * time.Minute)
	for {
		pods, err := client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{LabelSelector: "app=fourstep"})
		if err != nil {
			t.Fatalf("listing the tenant workload's pods: %v", err)
		}
		for i := range pods.Items {
			if pods.Items[i].Status.Phase == corev1.PodRunning {
				t.Logf("the tenant workload runs on %s", pods.Items[i].Spec.NodeName)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tenant workload never reached Running: %+v. The pod is what the node "+
				"was provisioned for, and a node that joins without it is a node the tenant cannot "+
				"use", pods.Items)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *fourstepStack) deleteWorkload(t *testing.T, ctx context.Context) {
	t.Helper()
	client, err := s.tenantClient()
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}
	policy := metav1.DeletePropagationBackground
	if err := client.AppsV1().Deployments("default").Delete(ctx, "fourstep", metav1.DeleteOptions{
		PropagationPolicy: &policy,
	}); err != nil {
		t.Fatalf("removing the tenant workload: %v", err)
	}
}

func (s *fourstepStack) listedNamespaces(t *testing.T, kubeconfig string) string {
	t.Helper()
	cmd := exec.Command(s.bins.kubectl, "--kubeconfig", kubeconfig,
		"--request-timeout=15s", "get", "namespaces", "--no-headers",
		"-o", "custom-columns=NAME:.metadata.name")
	var listed, complained strings.Builder
	cmd.Stdout = &listed
	cmd.Stderr = &complained
	if err := cmd.Run(); err != nil {
		t.Fatalf("listing the tenant cluster's namespaces at zero nodes: %v: %s -- the tenant "+
			"cluster's own objects live on its apiserver, so a namespace list that cannot be read "+
			"is a control plane that has stopped answering", err, strings.TrimSpace(complained.String()))
	}
	return strings.TrimSpace(listed.String())
}

func countLines(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(s), "\n"))
}

func TestTheFourStepProofRunsThroughTheCompositionRoot(t *testing.T) {
	want := strings.TrimSpace(os.Getenv(fourstepSubstrateEnv))
	ran := 0
	for _, substrate := range fourstepSubstrates() {
		if want != "" && want != substrate {
			continue
		}
		ran++
		t.Run(substrate, func(t *testing.T) { fourstepRun(t, substrate) })
	}
	if ran != 0 {
		return
	}
	livegate.Require(t, "%s is %q and this host is %s, which cannot run that substrate: %s. "+
		"Firecracker is a KVM guest and exists only on Linux, so a Windows host places nodes with "+
		"qemu only; a skip here is NOT a pass, and the run must name a substrate this host "+
		"supports", fourstepSubstrateEnv, want, hostos.Name(), strings.Join(fourstepSubstrates(), ", "))
}

func fourstepSubstrates() []string {
	out := []string{}
	if hostos.Firecracker() {
		out = append(out, SubstrateFirecracker)
	}
	return append(out, SubstrateQEMU)
}

func fourstepRun(t *testing.T, substrate string) {
	bins := fourstepRequirements(t, substrate)
	ctx, cancel := context.WithTimeout(context.Background(), fourstepDeadline)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := fourstepConfig(t, bins)
	stack := fourstepBringUp(t, ctx, cfg, bins, log)

	ready := stack.waitReady(t, ctx)
	t.Logf("STEP 1 the control plane reached %s at %s with zero nodes", ready.Phase, ready.URL)

	kubeconfig := stack.kubeconfig(t)
	stack.waitNodes(t, kubeconfig, 0)
	t.Log("STEP 2 `kubectl get nodes` is empty")

	emptyUntil := time.Now().Add(fourstepIdleWindow)
	passes := 0
	for time.Now().Before(emptyUntil) {
		dec := stack.pass(t, ctx)
		passes++
		if dec.Action != scale.None || dec.Nodes != 0 {
			t.Fatalf("an empty cluster decided %s with nodes=%d (%s). The intent rule exists so "+
				"that a tenant who asked for nothing is not given a node: k3s's own coredns, "+
				"metrics-server and local-path-provisioner are Pending on every cluster that has "+
				"ever booted, and a decision that counted them would provision a node for a "+
				"cluster nobody is using", dec.Action, dec.Nodes, dec.Reason)
		}
		time.Sleep(fourstepIdlePoll)
	}
	t.Logf("STEP 2 an empty cluster stays at zero nodes across %d passes %s apart over %s",
		passes, fourstepIdlePoll, fourstepIdleWindow)

	stack.applyWorkload(t, ctx)
	dec := stack.choose(t, ctx, scale.Provision)
	if dec.Nodes != 1 {
		t.Fatalf("the provisioning decision counted %d nodes, want 1: one tenant workload is one "+
			"node, and a second provision here is a second node (ADR 0014: idempotency is "+
			"per-cluster)", dec.Nodes)
	}

	joined := stack.waitNodes(t, kubeconfig, 1)
	stack.waitNodeReady(t, ctx)
	stack.assertCloudIdentity(t, kubeconfig)
	stack.logInstanceStartToReady(t, time.Now())
	t.Logf("STEP 3 the node joined on demand; `kubectl get nodes`:\n%s", joined)
	stack.waitWorkloadRunning(t, ctx)

	stack.deleteWorkload(t, ctx)
	stack.choose(t, ctx, scale.Teardown)
	stack.waitNodes(t, kubeconfig, 0)
	t.Log("STEP 4 the node is gone; `kubectl get nodes` is empty again")

	if _, err := stack.prov.Reconcile(ctx, stack.ref()); err != nil {
		t.Fatalf("STEP 4 the control plane stopped answering once the node was gone: %v -- a "+
			"cluster that returns to zero by dying has not returned to zero", err)
	}
	stillReady := stack.waitReady(t, ctx)
	namespaces := stack.listedNamespaces(t, kubeconfig)
	for _, want := range []string{"default", "kube-system"} {
		if !strings.Contains(namespaces, want) {
			t.Fatalf("the tenant cluster listed %q at zero nodes and %s is not among them: the "+
				"control plane's own objects are what the tenant's cluster IS, so a cluster that "+
				"returns to zero by losing them has not returned to zero", namespaces, want)
		}
	}
	t.Logf("STEP 4 the cluster is back at zero nodes, its control plane is still %s at %s, and it "+
		"still lists its namespaces:\n%s", stillReady.Phase, stillReady.URL, namespaces)

	if len(stack.registry.nodes) == 0 {
		t.Fatal("no node phase was ever written to the registry: the Node CR is the idempotency " +
			"record the scale decision re-reads, and a node materialised without one is a node " +
			"nothing can count and nothing will tear down")
	}
	last := stack.registry.nodes[len(stack.registry.nodes)-1]
	if last != v1alpha1.NodeDraining {
		t.Fatalf("the last node phase written was %s, want %s: the count the scale decision reads "+
			"is the tenant cluster's own node list, but the CR is what a restarted gateway finds",
			last, v1alpha1.NodeDraining)
	}
}
