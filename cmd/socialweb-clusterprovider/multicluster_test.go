//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

const (
	aliceDID = "did:plc:alice0000000000000000000000"
	bobDID   = "did:plc:bob000000000000000000000000"
)

func TestTwoAccountsNamingTheSameClusterGetDistinctIdentities(t *testing.T) {
	aliceHome := kcpclient.HomePath(aliceDID, "ci")
	bobHome := kcpclient.HomePath(bobDID, "ci")
	aliceKey, err := kcpclient.ClusterKey(aliceHome)
	if err != nil {
		t.Fatalf("alice's key: %v", err)
	}
	bobKey, err := kcpclient.ClusterKey(bobHome)
	if err != nil {
		t.Fatalf("bob's key: %v", err)
	}
	if aliceKey == bobKey {
		t.Fatalf("both accounts' %q resolved to the key %q. A cluster is named by its HOME "+
			"workspace, not by the bare name inside it: two accounts that each call a cluster "+
			"\"ci\" are two clusters, and one key for both is one state dir, one set of leases, "+
			"one node name and one mint credential shared between them", "ci", aliceKey)
	}
	for _, key := range []string{aliceKey, bobKey} {
		if key[:1] == "-" || len(key) >= 2 && key[len(key)-1:] == "-" {
			t.Fatalf("the key %q does not start and end alphanumeric, so no state dir, lease or "+
				"node name can carry it", key)
		}
	}

	stateDir := t.TempDir()
	aliceDir, err := clusterprovider.ClusterDataDir(stateDir, aliceKey)
	if err != nil {
		t.Fatalf("alice's state dir: %v", err)
	}
	bobDir, err := clusterprovider.ClusterDataDir(stateDir, bobKey)
	if err != nil {
		t.Fatalf("bob's state dir: %v", err)
	}
	if aliceDir == bobDir {
		t.Fatalf("both clusters' state dirs are %s", aliceDir)
	}

	aliceNode, err := nodeprovider.NodeName(aliceKey, "node1")
	if err != nil {
		t.Fatalf("alice's node name: %v", err)
	}
	bobNode, err := nodeprovider.NodeName(bobKey, "node1")
	if err != nil {
		t.Fatalf("bob's node name: %v", err)
	}
	if aliceNode == bobNode {
		t.Fatalf("both clusters' first node is named %s: the node names are the tenant "+
			"cluster's own Node objects and the node records under the state dir, so one name for "+
			"both is one node counted twice", aliceNode)
	}

	ports := testAllocator(t)
	aliceLease, err := ports.Allocate(portallocator.Request{
		Cluster: aliceKey, Purpose: portallocator.PurposeK3sSecure,
	})
	if err != nil {
		t.Fatalf("allocating alice's apiserver port: %v", err)
	}
	bobLease, err := ports.Allocate(portallocator.Request{
		Cluster: bobKey, Purpose: portallocator.PurposeK3sSecure,
	})
	if err != nil {
		t.Fatalf("allocating bob's apiserver port: %v", err)
	}
	if aliceLease.Port == bobLease.Port {
		t.Fatalf("both clusters hold port %d: a colliding apiserver port routes one cluster's "+
			"node at the other's apiserver, which surfaces as an x509 name error", aliceLease.Port)
	}
}

func TestAnAllClustersPassActsOnEveryClusterInTheTree(t *testing.T) {
	running := func(name string) v1alpha1.Cluster {
		return v1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
			Spec:       v1alpha1.ClusterSpec{Account: aliceDID, Running: true},
		}
	}
	off := running("off")
	off.Spec.Running = false

	f := &fakeClusters{items: []v1alpha1.Cluster{running("one"), running("two"), off}}
	host := newRecordingHost()
	p, err := Build(Config{
		Join:            JoinAllClusters,
		LeasePath:       filepath.Join(t.TempDir(), "ports.json"),
		GrantPath:       DefaultGrantPath,
		MintKubeconfigs: t.TempDir(),
		StateDir:        t.TempDir(),
		StoreEndpoint:   "postgres://postgres@127.0.0.1:25432/k3s?sslmode=disable",
	}, allClustersDeps(t, f, host), testLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	p.passAll(context.Background())

	want := []string{clusterKey(t, "one"), clusterKey(t, "two")}
	if got := host.spawned(); !slices.Equal(got, want) {
		t.Fatalf("the pass brought up %v, want %v: a Cluster with spec.running true is the only "+
			"one this process may hold a VM for", got, want)
	}
	if got := host.reaped(); !slices.Contains(got, clusterKey(t, "off")) {
		t.Fatalf("the OFF cluster was not reaped (reaped %v). spec.running=false is a field, not "+
			"a deletion: the CR still exists, so nothing else stops the group this process holds "+
			"for it and the apiserver keeps serving a cluster its owner switched off", got)
	}
	if got := host.spawned(); slices.Contains(got, clusterKey(t, "off")) {
		t.Fatalf("the OFF cluster was spawned anyway: %v", got)
	}
	if f.writes != 2 {
		t.Fatalf("%d status writes for three clusters, want one for each of the two this pass "+
			"brought up", f.writes)
	}
}

func TestAFailingClusterDoesNotHoldBackTheOthers(t *testing.T) {
	f := &fakeClusters{items: []v1alpha1.Cluster{
		{ObjectMeta: metav1.ObjectMeta{Name: "one", Generation: 1},
			Spec: v1alpha1.ClusterSpec{Account: aliceDID, Running: true}},
		{ObjectMeta: metav1.ObjectMeta{Name: "broken", Generation: 1},
			Spec: v1alpha1.ClusterSpec{Account: aliceDID, Running: true}},
		{ObjectMeta: metav1.ObjectMeta{Name: "three", Generation: 1},
			Spec: v1alpha1.ClusterSpec{Account: aliceDID, Running: true}},
	}}
	registry := &unreadableClusters{fakeClusters: f, name: "broken"}
	host := newRecordingHost()
	p, err := Build(Config{
		Join:            JoinAllClusters,
		LeasePath:       filepath.Join(t.TempDir(), "ports.json"),
		GrantPath:       DefaultGrantPath,
		MintKubeconfigs: t.TempDir(),
		StateDir:        t.TempDir(),
		StoreEndpoint:   "postgres://postgres@127.0.0.1:25432/k3s?sslmode=disable",
	}, allClustersDeps(t, registry, host), testLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	p.passAll(context.Background())

	want := []string{clusterKey(t, "one"), clusterKey(t, "three")}
	if got := host.spawned(); !slices.Equal(got, want) {
		t.Fatalf("a cluster whose CR could not be read held back the pass: it brought up %v, "+
			"want %v. One cluster that cannot come up must never stop the others, or a single bad "+
			"cluster freezes every cluster in the tree", got, want)
	}
}

func TestAMarketProvisionReturnsBeforeTheMachineExists(t *testing.T) {
	claims := &marketClaims{}
	tenant, _ := tenantClientFor(t)
	opts := marketOptions(t, claims, tenant)
	opts.JoinTimeout = NodeJoinTimeout
	hosts, err := newNodeHosts(opts)
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	nodes, err := hosts.forCluster(clusterKey(t, "liveweb"))
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}

	start := time.Now()
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed >= time.Second {
		t.Fatalf("a market Provision took %s with no tenant Node present. A market machine is "+
			"absent for minutes, so the pass waited out the %s join timeout inside a process that "+
			"serves every cluster in the tree: one new claim blocks every other cluster for the "+
			"whole timeout", elapsed, NodeJoinTimeout)
	}
	if got := claims.held(); got != 1 {
		t.Fatalf("%d claims after Provision, want exactly one: returning early must not skip the "+
			"claim, or the scale decision asks again on every pass for ever", got)
	}
}

func TestAMarketNodeIsConfirmedOnALaterPass(t *testing.T) {
	claims := &marketClaims{}
	nodeName := clusterKey(t, "liveweb") + "-node1"
	tenant, client := tenantClientFor(t, nodeName)
	opts := marketOptions(t, claims, tenant)
	opts.JoinTimeout = NodeJoinTimeout
	hosts, err := newNodeHosts(opts)
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	nodes, err := hosts.forCluster(clusterKey(t, "liveweb"))
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.setPhase(testHomePrefix+"liveweb", nodeName, v1alpha1.NodeClaimRunning)

	if err := hosts.confirm(ctx, clusterKey(t, "liveweb")); err != nil {
		t.Fatalf("confirm on a later pass: %v", err)
	}
	if !tenantNode(t, client, nodeName) {
		t.Fatalf("the tenant cluster reports node %s and the later pass did not confirm it", nodeName)
	}
	if got := hosts.inFlight(ctx, clusterKey(t, "liveweb")); got != 0 {
		t.Fatalf("%d nodes still counted in flight after the claim reached Running and the tenant "+
			"cluster reported the Node", got)
	}
}

func TestAMarketNodeWhoseClaimFailedIsTornDownOnALaterPass(t *testing.T) {
	claims := &marketClaims{}
	tenant, client := tenantClientFor(t)
	opts := marketOptions(t, claims, tenant)
	opts.JoinTimeout = NodeJoinTimeout
	hosts, err := newNodeHosts(opts)
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	key := clusterKey(t, "liveweb")
	nodes, err := hosts.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claim := claims.claims[testHomePrefix+"liveweb/"+key+"-node1"]
	claim.Status.Phase = v1alpha1.NodeClaimFailed
	claim.Status.Message = "the market refused every bid"
	claims.claims[testHomePrefix+"liveweb/"+key+"-node1"] = claim

	err = hosts.confirm(ctx, key)
	if err == nil {
		t.Fatal("a claim the market FAILED was carried into the next pass as if it were still " +
			"working: nothing fails the node, so the cluster counts a machine it will never get")
	}
	if got := claims.held(); got != 0 {
		t.Fatalf("%d claims survived the failed node", got)
	}
	if tenantNode(t, client, key+"-node1") {
		t.Fatal("the tenant Node object survived a failed claim")
	}
}

func TestALegacyStateDirIsMigratedToTheKey(t *testing.T) {
	stateDir := t.TempDir()
	mintDir := t.TempDir()
	f := &fakeClusters{items: []v1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: "liveweb"},
		Spec:       v1alpha1.ClusterSpec{Account: aliceDID},
	}}}
	key := clusterKey(t, "liveweb")

	legacyInstance, err := clusterprovider.InstanceName("liveweb")
	if err != nil {
		t.Fatalf("InstanceName: %v", err)
	}
	legacyClusterDir := filepath.Join(stateDir, legacyInstance)
	legacyNodeDir := filepath.Join(stateDir, "liveweb", clusterprovider.NodesDirName, "liveweb-node1")
	for _, dir := range []string{legacyClusterDir, legacyNodeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	legacyMint := filepath.Join(mintDir, "liveweb")
	if err := os.WriteFile(legacyMint, []byte("x"), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	ports := testAllocator(t)
	if _, err := ports.Allocate(portallocator.Request{
		Cluster: "liveweb", Purpose: portallocator.PurposeK3sSecure,
	}); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	networks := testNetworks(t)
	if _, err := networks.Allocate(subnetallocator.Request{Cluster: "liveweb"}); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	if err := migrateLegacyKeys(context.Background(), f, legacyKeys{
		StateDir: stateDir,
		MintDir:  mintDir,
		Ports:    ports,
		Networks: networks,
		Log:      testLogger(),
	}); err != nil {
		t.Fatalf("migrateLegacyKeys: %v", err)
	}

	keyedInstance, err := clusterprovider.InstanceName(key)
	if err != nil {
		t.Fatalf("InstanceName: %v", err)
	}
	for _, want := range []string{
		filepath.Join(stateDir, keyedInstance),
		filepath.Join(stateDir, key, clusterprovider.NodesDirName, "liveweb-node1"),
		filepath.Join(mintDir, key),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("after the migration %s is missing: %v. A deployment that joined under the "+
				"bare name keeps its state dir, its node records, its leases and its mint "+
				"credential, or the first pass reads an empty state dir over a cluster that is "+
				"already running", want, err)
		}
	}
	if _, ok := ports.PortFor("liveweb", portallocator.PurposeK3sSecure); ok {
		t.Fatal("the port lease was not re-keyed")
	}
	if _, ok := ports.PortFor(key, portallocator.PurposeK3sSecure); !ok {
		t.Fatal("the port lease did not move to the cluster's key, so the running group's port " +
			"would be reclaimed and handed to another cluster while its process still binds it")
	}
	if _, ok := networks.LeaseFor(key); !ok {
		t.Fatal("the subnet lease did not move to the cluster's key")
	}
}

func TestAnAmbiguousNameIsNotMigrated(t *testing.T) {
	stateDir := t.TempDir()
	f := &fakeClusters{items: []v1alpha1.Cluster{
		{ObjectMeta: metav1.ObjectMeta{Name: "ci"}, Spec: v1alpha1.ClusterSpec{Account: aliceDID}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ci"}, Spec: v1alpha1.ClusterSpec{Account: bobDID}},
	}}
	legacy, err := clusterprovider.InstanceName("ci")
	if err != nil {
		t.Fatalf("InstanceName: %v", err)
	}
	dir := filepath.Join(stateDir, legacy)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	if err := migrateLegacyKeys(context.Background(), f, legacyKeys{
		StateDir: stateDir,
		Log:      testLogger(),
	}); err != nil {
		t.Fatalf("migrateLegacyKeys: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a state dir named by a cluster name two accounts share was moved: %v. Which "+
			"account's cluster it belongs to is not knowable from the name alone, and renaming it "+
			"onto one of them hands that cluster the other's data", err)
	}
}

type recordingHost struct {
	mu      sync.Mutex
	live    map[string]bool
	brought []string
	stopped []string
}

func newRecordingHost() *recordingHost {
	return &recordingHost{live: map[string]bool{}}
}

func (h *recordingHost) Live(cluster string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[cluster]
}

func (h *recordingHost) Spawn(_ context.Context, cluster string, steps []procgroup.Step) (*procgroup.Group, error) {
	if len(steps) == 0 {
		return nil, errors.New("recordingHost: a spawn with no steps")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.live[cluster] = true
	h.brought = append(h.brought, cluster)
	return nil, nil
}

func (h *recordingHost) Reap(_ context.Context, cluster string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.live, cluster)
	if !slices.Contains(h.stopped, cluster) {
		h.stopped = append(h.stopped, cluster)
	}
	return nil
}

func (h *recordingHost) Alive(string) []string { return nil }

func (h *recordingHost) Kill(context.Context, string) error { return nil }

func (h *recordingHost) spawned() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]string(nil), h.brought...)
	slices.Sort(out)
	return out
}

func (h *recordingHost) reaped() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.stopped...)
}

type unreadableClusters struct {
	*fakeClusters
	name string
}

func (u *unreadableClusters) Read(_ context.Context, ref clusterprovider.Ref) (*v1alpha1.Cluster, error) {
	if ref.Name == u.name {
		return nil, errors.New("unreadableClusters: this cluster's CR cannot be read")
	}
	return u.fakeClusters.Read(context.Background(), ref)
}

func allClustersDeps(t *testing.T, registry clusterprovider.Clusters, host materialiser.Host) Deps {
	t.Helper()
	a := testAllocator(t)
	return Deps{
		Registry:       registry,
		Decider:        spawningDecider(t, a, host),
		Allocator:      a,
		Host:           host,
		PostgresNeeded: false,
		NewInstance:    func() string { return "gen-test-1" },
		Tenant: func(context.Context, clusterprovider.Ref, *v1alpha1.Cluster) (*rest.Config, error) {
			return &rest.Config{Host: "https://127.0.0.1:16445"}, nil
		},
	}
}

func spawningDecider(t *testing.T, a *portallocator.Allocator, host materialiser.Host) *materialiser.Reconciler {
	t.Helper()
	rec, err := materialiser.New(materialiser.Options{
		Host:     host,
		Ports:    a,
		Networks: testNetworks(t),
		Steps: func(materialiser.Observed, materialiser.Leases) ([]procgroup.Step, error) {
			return []procgroup.Step{{
				Name: procgroup.ComponentK3s,
				Spec: processrunner.Spec{Argv: []string{"/bin/true"}},
			}}, nil
		},
		Identity:    func(context.Context, materialiser.Observed) error { return nil },
		NewInstance: instanceMinter(),
		Now:         time.Now,
		Purposes:    []portallocator.Purpose{portallocator.PurposeK3sSecure},
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}
	return rec
}
