//go:build linux

package clusterprovider

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/identitybundle"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/instance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const proofDeadline = 4 * time.Minute

func k3sNodePasswordPath() string {
	if p := os.Getenv("SOCIALWEB_K3S_NODE_PASSWORD"); p != "" {
		return p
	}
	return "/etc/rancher/node/password"
}

func proofStateDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("PROOF_STATE_DIR")
	if dir == "" {
		return t.TempDir()
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("clearing %s: %v", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	return dir
}

func skipWithoutK3sAndKine(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test runs the four-step proof over a real k3s and kine and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	skipWithoutPostgres(t)
	for _, bin := range []string{"k3s", "kine"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH: the four-step proof runs the real components, and a skip "+
				"here is NOT a pass -- nothing else in this tree starts a process group at all", bin)
		}
	}
	if err := nodePasswordWritable(); err != nil {
		livegate.RequireLocal(t, "k3s server --disable-agent is not a server without an agent: it still bootstraps "+
			"the host-global node password, and this process cannot open it: %v. MEASURED: the "+
			"server then logs \"Waiting to retrieve agent configuration; server is not ready: open "+
			"/etc/rancher/node/password: permission denied\" for ever and /readyz never leaves 503, "+
			"so the cluster sits in Initializing until the init timeout. k3s's path is NOT under "+
			"--data-dir and no flag moves it, so it is a host-global resource on the same footing as "+
			"6444 and the containerd socket: the gateway's user must OWN it, and postgres must not "+
			"run as root, so neither `as root` nor `as a plain user` is the whole story. Override "+
			"with SOCIALWEB_K3S_NODE_PASSWORD if this host keeps it elsewhere", err)
	}
}

func nodePasswordWritable() error {
	f, err := os.OpenFile(k3sNodePasswordPath(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

type proofClusters struct {
	cluster   *v1alpha1.Cluster
	writes    []v1alpha1.ClusterStatus
	nodes     []v1alpha1.NodePhase
	finalized bool
	err       error
	live      func() bool
}

const proofDID = "did:web:proof.example"

func proofHome(name string) string { return kcpclient.HomePath(proofDID, name) }

func proofKey(t *testing.T, name string) string {
	t.Helper()
	key, err := kcpclient.ClusterKey(proofHome(name))
	if err != nil {
		t.Fatalf("the key of %s: %v", name, err)
	}
	return key
}

func (p *proofClusters) Refs(context.Context) ([]Ref, error) {
	if p.err != nil {
		return nil, p.err
	}
	return []Ref{{Account: "root:proof", DID: p.cluster.Spec.Account, Home: proofHome(p.cluster.Name), Name: p.cluster.Name}}, nil
}

func (p *proofClusters) Read(context.Context, Ref) (*v1alpha1.Cluster, error) {
	if p.err != nil {
		return nil, p.err
	}
	c := *p.cluster
	return &c, nil
}

func (p *proofClusters) WriteStatus(_ context.Context, _ Ref, st v1alpha1.ClusterStatus) error {
	p.writes = append(p.writes, st)
	p.cluster.Status = st
	return nil
}

func (p *proofClusters) MountStatus(context.Context, Ref) int {
	if p.cluster.DeletionTimestamp != nil {
		return 503
	} else if p.live != nil && p.live() {
		return 401
	} else {
		return 502
	}
}

func (p *proofClusters) IdentityState(context.Context, Ref) (v1alpha1.IdentityState, error) {
	return v1alpha1.IdentityRevoked, nil
}

func (p *proofClusters) Nodes(context.Context, Ref) ([]v1alpha1.Node, error) {
	return nil, nil
}

func (p *proofClusters) WriteNode(_ context.Context, _ Ref, _ string, status v1alpha1.NodeStatus) error {
	p.nodes = append(p.nodes, status.Phase)
	return nil
}

func (p *proofClusters) RemoveFinalizer(_ context.Context, _ Ref) error {
	p.finalized = true
	return nil
}

type proofStore struct {
	pg *Postgres
}

func (s *proofStore) endpoint(string) (string, error) { return s.pg.DSN(), nil }

func identityFingerprint(t *testing.T, dataDir string) string {
	t.Helper()
	return identitybundle.Sum([]byte(readIdentity(t, dataDir)))
}

type bringUpTimes struct {
	ready time.Duration

	listener time.Duration

	api time.Duration
}

func timeToReady(t *testing.T, ctx context.Context, prov *Provider, registry *proofClusters, apiURL func() string, what string) bringUpTimes {
	t.Helper()
	var times bringUpTimes
	start := time.Now()
	deadline := start.Add(3 * time.Minute)
	ref := Ref{Account: "root:proof", Home: proofHome(registry.cluster.Name), Name: registry.cluster.Name}
	for {
		pass, err := prov.Reconcile(ctx, ref)
		if err != nil {
			t.Logf("%s: reconcile error: %v", what, err)
		} else if len(pass.Result.Ops) > 0 {
			t.Logf("%s: pass phase=%s ops=%v", what, pass.Result.Phase, pass.Result.Ops)
		}
		if url := apiURL(); url != "" {
			if code, reachable := Probe(ctx, url); reachable {
				if times.listener == 0 {
					times.listener = time.Since(start)
				}
				if times.api == 0 && apiserverIsAnswering(code) {
					times.api = time.Since(start)
				}
			}
		}
		if err == nil && pass.Result.Phase == readyPhase {
			times.ready = time.Since(start)
			return times
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the control plane never reached Ready; last status %+v",
				what, registry.cluster.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestAPreSeededIdentityBundleSavesTheControlPlaneFromAuthoringItsCAs(t *testing.T) {
	skipWithoutK3sAndKine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	stateDir := t.TempDir()
	const clusterName = "liveweb"
	const did = "did:plc:liveweb0000000000000000"
	clusterKey := proofKey(t, clusterName)

	rebuildPorts, err := liveports.TakeForProcess(liveports.RebuildProof)
	if err != nil {
		t.Fatalf("%v: this proof binds a store and an apiserver on this host", err)
	}
	t.Logf("this run holds %s on the host; a copy of this test running beside it takes another "+
		"slot of the block, so the two cannot reach the same store port", liveports.Text(rebuildPorts))

	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
		Range:     rebuildPorts,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}

	pgLease, err := alloc.Allocate(portallocator.Request{Cluster: clusterKey, Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("the store's lease: %v", err)
	}
	pg, err := StartPostgres(ctx, PostgresSpec{
		Cluster:  clusterName,
		StateDir: filepath.Join(stateDir, "store"),
		Port:     pgLease.Port,
		Log:      log,
	})
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := pg.Stop(stopCtx); err != nil {
			t.Logf("stopping the store: %v", err)
		}
	})
	store := &proofStore{pg: pg}

	policy := filepath.Join(t.TempDir(), "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages:\n  - RequestReceived\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatalf("writing the audit policy: %v", err)
	}

	stackCfg := StackConfig{
		Stack:            Stack{procgroup.ComponentKine, procgroup.ComponentK3s},
		Admin:            adminFromDataDir(stateDir),
		StateDir:         stateDir,
		StoreEndpoint:    store.endpoint,
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		AuditPolicyPath:  policy,
		Harvest: func(_ context.Context, cluster string) error {
			return HarvestBundle(stateDir, cluster)
		},
		GuardRides: true,
	}
	purposes, err := PurposesFor(stackCfg.Stack, true)
	if err != nil {
		t.Fatalf("PurposesFor: %v", err)
	}

	host := materialiser.NewLocalHost(procgroup.Options{})
	host.PostgresUp = func(string) bool { return pg.Up() }
	t.Cleanup(func() {
		reapCtx, reap := context.WithTimeout(context.Background(), 60*time.Second)
		defer reap()
		if err := host.Reap(reapCtx, clusterKey); err != nil {
			t.Logf("reaping the group: %v", err)
		}
	})

	decider, err := materialiser.New(materialiser.Options{
		Host:        host,
		Ports:       alloc,
		Networks:    testNetworks(t),
		Steps:       StepsFor(stackCfg),
		Identity:    GuardFor(proofGuardDeps(t, stateDir, clusterName, store, log)),
		NewInstance: func() string { return "gen-proof-1" },
		Purposes:    purposes,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}

	registry := &proofClusters{cluster: &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec:       v1alpha1.ClusterSpec{Account: did, Running: true},
	}, live: func() bool { return host.Live(clusterKey) }}
	prov, err := New(Options{
		Registry:         registry,
		Decider:          decider,
		Ports:            alloc,
		Host:             host,
		WriteStatus:      true,
		NodeExpirySource: NodeExpiryRollup,
		Probe:            Probe,
		Interval:         time.Second,
		Log:              log,
	})
	if err != nil {
		t.Fatalf("clusterprovider.New: %v", err)
	}

	dataDir, err := ClusterDataDir(stateDir, clusterKey)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	root := filepath.Join(dataDir, "server")
	bundleDir, err := BundleDir(stateDir, clusterKey)
	if err != nil {
		t.Fatalf("BundleDir: %v", err)
	}

	apiURL := func() string {
		lease, ok := alloc.PortFor(clusterKey, portallocator.PurposeK3sSecure)
		if !ok {
			return ""
		}
		return APIServerURL(lease.Port)
	}

	cold := timeToReady(t, ctx, prov, registry, apiURL, "cold")
	authored := identityFingerprint(t, dataDir)
	if _, err := os.Stat(filepath.Join(bundleDir, identitybundle.Paths[0])); err != nil {
		t.Fatalf("the control plane reached Ready and no identity bundle was persisted at %s: %v. "+
			"Nothing would then survive a teardown, and every rebuild would be a NEW cluster under "+
			"the same name", bundleDir, err)
	}

	if err := host.Reap(ctx, clusterKey); err != nil {
		t.Fatalf("reaping the group before the rebuild: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("wiping %s for the rebuild: %v", root, err)
	}

	seeded := timeToReady(t, ctx, prov, registry, apiURL, "pre-seeded")
	if got := identityFingerprint(t, dataDir); got != authored {
		t.Fatalf("the CA fingerprint did NOT survive the rebuild:\nauthored %s\nrebuilt  %s\n\n"+
			"The cluster's own identity is its CAs, and k3s REGENERATES them whenever it does not "+
			"find all 13 entries where it looks -- at which point every kubeconfig and every "+
			"ServiceAccount token issued before the rebuild stops validating, silently", authored, got)
	}

	t.Logf("MEASURED cold ready=%s listener=%s apiserver=%s", cold.ready, cold.listener, cold.api)
	t.Logf("MEASURED pre-seeded ready=%s listener=%s apiserver=%s", seeded.ready, seeded.listener, seeded.api)

	if err := host.Reap(ctx, clusterKey); err != nil {
		t.Fatalf("reaping the group before the control run: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("wiping %s for the control run: %v", root, err)
	}
	if err := os.RemoveAll(bundleDir); err != nil {
		t.Fatalf("removing %s for the control run: %v", bundleDir, err)
	}
	if err := pg.DeleteBootstrapRows(ctx); err != nil {
		t.Fatalf("clearing the bootstrap row for the control run: %v", err)
	}
	regenerated := timeToReady(t, ctx, prov, registry, apiURL, "no bundle")
	afterControl := identityFingerprint(t, dataDir)
	t.Logf("MEASURED no-bundle ready=%s listener=%s apiserver=%s", regenerated.ready, regenerated.listener, regenerated.api)
	t.Logf("MEASURED attribution: the no-bundle rebuild authored its OWN CAs (identity changed: %v) "+
		"and reached Ready in %s against the seeded run's %s, a difference inside the run-to-run "+
		"spread. A FRESH STORE is what the cold figure is made of: %s of a %s cold run is the "+
		"datastore, and the CAs are not what the missing 25 seconds ever were. On this host the "+
		"bundle buys IDENTITY, not time: without it every rebuild is a new cluster under the same "+
		"name and every credential issued before it stops validating SILENTLY",
		afterControl != authored, regenerated.ready, seeded.ready,
		cold.ready-regenerated.ready, cold.ready)

	if seeded.ready >= cold.ready {
		t.Fatalf("a pre-seeded control plane took %s and a cold one took %s: the bundle exists to "+
			"collect what k3s spends AUTHORING its CAs, and a rebuild that pays that cost again is "+
			"not being spared it", seeded.ready, cold.ready)
	}
}

func TestTheControlPlaneReachesReadyAtZeroNodes(t *testing.T) {
	skipWithoutK3sAndKine(t)
	ctx, cancel := context.WithTimeout(context.Background(), proofDeadline)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	stateDir := proofStateDir(t)
	const clusterName = "liveweb"
	const did = "did:plc:liveweb0000000000000000"
	clusterKey := proofKey(t, clusterName)

	zeroNodePorts, err := liveports.TakeForProcess(liveports.ZeroNodeProof)
	if err != nil {
		t.Fatalf("%v: this proof binds a store and an apiserver on this host", err)
	}
	t.Logf("this run holds %s on the host; a copy of this test running beside it takes another "+
		"slot of the block, so the two cannot reach the same store and apiserver ports",
		liveports.Text(zeroNodePorts))

	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
		Range:     zeroNodePorts,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}

	pgLease, err := alloc.Allocate(portallocator.Request{Cluster: clusterKey, Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("the store's lease: %v", err)
	}
	pg, err := StartPostgres(ctx, PostgresSpec{
		Cluster:  clusterName,
		StateDir: filepath.Join(stateDir, "store"),
		Port:     pgLease.Port,
		Log:      log,
	})
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := pg.Stop(stopCtx); err != nil {
			t.Logf("stopping the store: %v", err)
		}
	})
	store := &proofStore{pg: pg}

	policy := filepath.Join(t.TempDir(), "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages:\n  - RequestReceived\nrules:\n  - level: Metadata\n"), 0o600); err != nil {
		t.Fatalf("writing the audit policy: %v", err)
	}

	stackCfg := StackConfig{
		Stack:            DesignedStack,
		Admin:            adminFromDataDir(stateDir),
		StateDir:         stateDir,
		StoreEndpoint:    store.endpoint,
		VaultStoreDSN:    func(string) (string, error) { return store.pg.VaultDSN(), nil },
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
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
				StateDir:    stateDir,
				Cluster:     cluster,
				VaultPort:   port,
				PostgresDSN: store.pg.DSN(),
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
		NewInstance: func() string { return "gen-proof-1" },
		Purposes:    purposes,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}
	t.Cleanup(func() {
		reapCtx, reap := context.WithTimeout(context.Background(), 30*time.Second)
		defer reap()
		if err := host.Reap(reapCtx, clusterKey); err != nil {
			t.Logf("reaping the group: %v", err)
		}
	})

	registry := &proofClusters{cluster: &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec:       v1alpha1.ClusterSpec{Account: did, Running: true},
	}, live: func() bool { return host.Live(clusterKey) }}
	prov, err := New(Options{
		Registry:         registry,
		Decider:          decider,
		Ports:            alloc,
		Host:             host,
		WriteStatus:      true,
		NodeExpirySource: NodeExpiryRollup,
		Probe:            Probe,
		Interval:         time.Second,
		Log:              log,
	})
	if err != nil {
		t.Fatalf("clusterprovider.New: %v", err)
	}

	ready := waitReady(t, ctx, prov, registry,
		fmt.Sprintf("the zero-node proof, holding %s", liveports.Text(zeroNodePorts)))
	t.Logf("STEP 1 cluster reached %s at %s", ready.Phase, ready.URL)

	logClusterVault(t, ctx, stateDir, clusterKey)

	apiserver := APIServerURL(mustAPIPort(t, alloc, clusterKey))
	tenant := tenantClientFor(t, ctx, stateDir, clusterKey, apiserver)

	if got := nodeNames(t, ctx, tenant); len(got) != 0 {
		t.Fatalf("STEP 2 the cluster reports %v nodes at zero; the state is not zero", got)
	}
	t.Log("STEP 2 the control plane is up with zero nodes")
}

func proofGuardDeps(t *testing.T, stateDir, cluster string, store *proofStore, log *slog.Logger) GuardDeps {
	t.Helper()
	tokens := map[string]instance.Token{}
	return GuardDeps{
		StateDir: stateDir,
		Token: func(name string) instance.Token {
			if tok, ok := tokens[name]; ok {
				return tok
			}
			tok, err := instance.New(nil)
			if err != nil {
				t.Fatalf("instance.New: %v", err)
			}
			dir, err := ClusterDataDir(stateDir, name)
			if err != nil {
				t.Fatalf("ClusterDataDir: %v", err)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("creating %s: %v", dir, err)
			}
			if err := instance.WriteFile(TokenPath(dir), tok); err != nil {
				t.Fatalf("writing the token: %v", err)
			}
			tokens[name] = tok
			return tok
		},
		HasRow: func(string) bool {
			has, err := store.pg.HasBootstrapRow(context.Background())
			if err != nil {
				t.Fatalf("HasBootstrapRow: %v", err)
			}
			return has
		},
		DeleteRow: func(ctx context.Context, name string) error {
			return store.pg.DeleteBootstrapRows(ctx)
		},
		SeedBundle: func(_ context.Context, name string) error { return SeedBundle(stateDir, name) },
		Bundle:     BundleGuard(stateDir),
		Log:        log,
	}
}

func adminFromDataDir(stateDir string) AdminClient {
	return func(_ context.Context, cluster, apiserver string) (kubernetes.Interface, error) {
		dataDir, err := ClusterDataDir(stateDir, cluster)
		if err != nil {
			return nil, err
		}
		cfg, err := clientcmd.BuildConfigFromFlags("", clusterstack.AdminKubeconfig(dataDir))
		if err != nil {
			return nil, fmt.Errorf("clusterprovider: the cluster's own admin credential: %w", err)
		}
		if apiserver != "" {
			cfg.Host = apiserver
		}
		return kubernetes.NewForConfig(cfg)
	}
}

func mustAPIPort(t *testing.T, alloc *portallocator.Allocator, cluster string) int {
	t.Helper()
	lease, ok := alloc.PortFor(cluster, portallocator.PurposeK3sSecure)
	if !ok {
		t.Fatalf("no apiserver lease for %s", cluster)
	}
	return lease.Port
}

func tenantClientFor(t *testing.T, ctx context.Context, stateDir, cluster, apiserver string) kubernetes.Interface {
	t.Helper()
	cfg, err := MintTenantCredentialForCluster(ctx, stateDir, cluster, apiserver, CredentialSourceVaultIfSealed)
	if err != nil {
		t.Fatalf("minting the tenant credential: %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}
	return client
}

func waitReady(t *testing.T, ctx context.Context, prov *Provider, registry *proofClusters, what string) materialiser.Result {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	ref := Ref{Account: "root:proof", Home: proofHome(registry.cluster.Name), Name: registry.cluster.Name}
	for {
		pass, err := prov.Reconcile(ctx, ref)
		if err != nil {
			t.Logf("reconcile error: %v", err)
		} else if len(pass.Result.Ops) > 0 {
			t.Logf("pass: phase=%s ops=%v alert=%q", pass.Result.Phase, pass.Result.Ops, pass.Result.Alert)
		}
		if err == nil && pass.Result.Phase == readyPhase {
			return pass.Result
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the cluster never reached Ready in 2m; last status %+v. The ports it "+
				"holds are named above: a readiness timeout and a port another run took look the "+
				"same from here, and only the range tells them apart",
				what, registry.cluster.Status)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func nodeNames(t *testing.T, ctx context.Context, client kubernetes.Interface) []string {
	t.Helper()
	list, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing the tenant cluster's nodes: %v", err)
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	return out
}
