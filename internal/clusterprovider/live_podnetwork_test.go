//go:build linux

package clusterprovider

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantnet"
)

func nodeMayListNodes(t *testing.T, ctx context.Context, core kubernetes.Interface) (bool, string) {
	t.Helper()
	sar, err := core.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:               "system:node:podnet-node1",
			Groups:             []string{"system:nodes", "system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: "list", Resource: "nodes"},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("SubjectAccessReview: %v", err)
	}
	return sar.Status.Allowed, sar.Status.Reason
}

func TestAZeroNodeMarketClusterAllocatesPodCIDRsAndTakesTheNodeNetworkAddOnsAsBaseline(t *testing.T) {
	skipWithoutK3sAndKine(t)
	skipWithoutBao(t)
	ctx, cancel := context.WithTimeout(context.Background(), proofDeadline)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	stateDir := proofStateDir(t)
	const clusterName = "podnet"
	const did = "did:plc:podnet0000000000000000000"
	clusterKey := proofKey(t, clusterName)

	ports, err := liveports.TakeForProcess(liveports.ZeroNodeProof)
	if err != nil {
		t.Fatalf("%v: this proof binds a store and an apiserver", err)
	}
	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
		Range:     ports,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	pgLease, err := alloc.Allocate(portallocator.Request{Cluster: clusterKey, Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("the store's lease: %v", err)
	}
	pg, err := StartPostgres(ctx, PostgresSpec{
		Cluster: clusterName, StateDir: filepath.Join(stateDir, "store"), Port: pgLease.Port, Log: log,
	})
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = pg.Stop(stopCtx)
	})
	store := &proofStore{pg: pg}

	policy := filepath.Join(t.TempDir(), "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages:\n  - RequestReceived\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stackCfg := StackConfig{
		Stack:            append(Stack{}, DesignedStack...),
		Admin:            adminFromDataDir(stateDir),
		StateDir:         stateDir,
		StoreEndpoint:    store.endpoint,
		VaultStoreDSN:    func(string) (string, error) { return store.pg.VaultDSN(), nil },
		AdvertiseAddress: besideMarketAdvertise,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		MarketDialled:    true,
		VaultTLS:         testVaultTLS(),
		AuditPolicyPath:  policy,
		Harvest: func(ctx context.Context, cluster string) error {
			if err := HarvestBundle(stateDir, cluster); err != nil {
				return err
			}
			port := 0
			if lease, ok := alloc.PortFor(cluster, portallocator.PurposeOpenBao); ok {
				port = lease.Port
			}
			return CustodyClusterVault(ctx, CustodyOptions{
				StateDir: stateDir, Cluster: cluster, VaultPort: port, PostgresDSN: store.pg.DSN(),
			})
		},
		GuardRides: true,
	}
	purposes, err := PurposesFor(stackCfg.Stack, true)
	if err != nil {
		t.Fatalf("PurposesFor: %v", err)
	}
	host := materialiser.NewLocalHost(procgroup.Options{})
	host.PostgresUp = func(string) bool { return pg.Up() }
	decider, err := materialiser.New(materialiser.Options{
		Host:        host,
		Ports:       alloc,
		Networks:    testNetworks(t),
		Steps:       StepsFor(stackCfg),
		Identity:    GuardFor(proofGuardDeps(t, stateDir, clusterName, store, log)),
		NewInstance: func() string { return "gen-podnet-1" },
		Purposes:    purposes,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}
	t.Cleanup(func() {
		reapCtx, reap := context.WithTimeout(context.Background(), 30*time.Second)
		defer reap()
		_ = host.Reap(reapCtx, clusterKey)
	})
	registry := &proofClusters{cluster: &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec:       v1alpha1.ClusterSpec{Account: did, Running: true},
	}, live: func() bool { return host.Live(clusterKey) }}
	prov, err := New(Options{
		Registry: registry, Decider: decider, Ports: alloc, Host: host, WriteStatus: true,
		NodeExpirySource: NodeExpiryRollup, Probe: Probe, Interval: time.Second, Log: log,
	})
	if err != nil {
		t.Fatalf("clusterprovider.New: %v", err)
	}

	started := time.Now()
	ready := waitReady(t, ctx, prov, registry, fmt.Sprintf("the pod network proof, holding %s", liveports.Text(ports)))
	dataDir, err := ClusterDataDir(stateDir, clusterKey)
	if err != nil {
		t.Fatal(err)
	}
	k3sLog, err := os.ReadFile(filepath.Join(LogDir(dataDir), string(procgroup.ComponentK3s)+".log"))
	if err != nil {
		t.Fatalf("STEP 1 the k3s server's log: %v", err)
	}
	var controllerManager string
	for _, line := range strings.Split(string(k3sLog), "\n") {
		if strings.Contains(line, "Running kube-controller-manager") {
			controllerManager = line
		}
	}
	for _, want := range []string{"--allocate-node-cidrs=true", "--cluster-cidr=" + clusterstack.MarketClusterCIDR} {
		if !strings.Contains(controllerManager, want) {
			t.Fatalf("STEP 1 the controller manager k3s ran lacks %s, so no Node would get the podCIDR the "+
				"guest's bridge CNI waits for: %q", want, controllerManager)
		}
	}
	t.Logf("STEP 1 the market cluster reached %s in %s; its controller manager allocates node CIDRs from %s", ready.Phase,
		time.Since(started).Round(10*time.Millisecond), clusterstack.MarketClusterCIDR)

	apiserver := APIServerURL(mustAPIPort(t, alloc, clusterKey))
	core, dyn := adminRest(t, stateDir, clusterKey, apiserver)
	if allowed, reason := nodeMayListNodes(t, ctx, core); allowed {
		t.Fatalf("STEP 2 a node identity may list Nodes before the node reader is applied (%s): the proof "+
			"below would show nothing", reason)
	}

	if err := tenantnet.Apply(ctx, dyn, AddOnFieldManager); err != nil {
		t.Fatalf("STEP 3 tenantnet.Apply against the real apiserver: %v", err)
	}
	if err := tenantnet.Apply(ctx, dyn, AddOnFieldManager); err != nil {
		t.Fatalf("STEP 3 a second apply is not idempotent: %v", err)
	}
	allowed, reason := nodeMayListNodes(t, ctx, core)
	if !allowed {
		t.Fatalf("STEP 3 after the node reader a node identity still may not list Nodes: %s", reason)
	}
	t.Logf("STEP 3 the apiserver admitted every node network object twice; system:node:podnet-node1 may now list Nodes (%s)", reason)

	ds, err := core.AppsV1().DaemonSets("kube-system").Get(ctx, tenantnet.DNSName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("STEP 4 the per-node DNS DaemonSet: %v", err)
	}
	if ds.Spec.Template.Spec.NodeSelector[tenantnet.NodeLabel] != "true" || !ds.Spec.Template.Spec.HostNetwork {
		t.Fatalf("STEP 4 the stored DaemonSet lost its selector or host network: %+v", ds.Spec.Template.Spec)
	}
	b, err := MeasureAddOnBaseline(ctx, core, AddOnFieldManager, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range b.Workloads {
		found = found || (w.Kind == scale.KindDaemonSet && w.Namespace == "kube-system" && w.Name == tenantnet.DNSName)
	}
	if !found {
		t.Fatalf("STEP 4 the measured baseline %+v lacks the per-node DNS", b.Workloads)
	}
	obs, _, err := baselineObserver(core, &b).Observe(ctx, clusterKey)
	if err != nil {
		t.Fatal(err)
	}
	if scale.Intent(obs) {
		t.Fatalf("STEP 4 the node network add-ons read as intent at zero nodes (demand %d)", scale.Demand(obs))
	}
	nodes, err := core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) != 0 {
		t.Fatalf("STEP 4 the cluster left zero nodes: %d, %v", len(nodes.Items), err)
	}
	t.Logf("STEP 4 zero nodes; baseline %d workloads including %s/%s; no intent", len(b.Workloads), "kube-system", tenantnet.DNSName)
}
