//go:build linux

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scaledriver"
)

type marketClaims struct {
	mu sync.Mutex

	claims  map[string]v1alpha1.NodeClaim
	writes  int
	deletes int
}

func (c *marketClaims) WriteNodeClaim(_ context.Context, workspace string, claim v1alpha1.NodeClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	if c.claims == nil {
		c.claims = map[string]v1alpha1.NodeClaim{}
	}
	c.claims[workspace+"/"+claim.Name] = claim
	return nil
}

func (c *marketClaims) ReadNodeClaim(_ context.Context, workspace, name string) (*v1alpha1.NodeClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	claim, ok := c.claims[workspace+"/"+name]
	if !ok {
		return nil, nil
	}
	out := claim
	return &out, nil
}

func (c *marketClaims) ListNodeClaims(_ context.Context, workspace string) ([]v1alpha1.NodeClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []v1alpha1.NodeClaim{}
	for key, claim := range c.claims {
		if strings.HasPrefix(key, workspace+"/") {
			out = append(out, claim)
		}
	}
	return out, nil
}

func (c *marketClaims) DeleteNodeClaim(_ context.Context, workspace, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes++
	delete(c.claims, workspace+"/"+name)
	return nil
}

func (c *marketClaims) held() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.claims)
}

func (c *marketClaims) setPhase(workspace, name string, phase v1alpha1.NodeClaimPhase) {
	c.mu.Lock()
	defer c.mu.Unlock()
	claim := c.claims[workspace+"/"+name]
	claim.Status.Phase = phase
	c.claims[workspace+"/"+name] = claim
}

func marketRegistry() clusterprovider.Clusters {
	return &fakeClusters{items: []v1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: "liveweb"},
		Spec:       v1alpha1.ClusterSpec{Account: "did:web:example.com"},
	}}}
}

func marketOptions(t *testing.T, claims nodemarket.Claims, tenant tenantClient) nodeHostsOptions {
	t.Helper()
	return nodeHostsOptions{
		Allocator:           testAllocator(t),
		Networks:            testNetworks(t),
		Registry:            marketRegistry(),
		Claims:              claims,
		StateDir:            t.TempDir(),
		AdvertiseAddress:    portallocator.MarketAdvertiseAddress,
		ClaimReleaseTimeout: time.Second,
		JoinTimeout:         time.Nanosecond,
		Tenant:              tenant,
		Log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		Substrate:           nodeSubstrate{Name: SubstrateMarket},
	}
}

func intentTenant(t *testing.T) (tenantClient, kubernetes.Interface) {
	t.Helper()
	tenant, client := tenantClientFor(t)
	_, err := client.CoreV1().Pods("default").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seeding the pod the scale decision reads as intent: %v", err)
	}
	return tenant, client
}

func TestTheMarketSubstrateNeedsNoBinaryOnThisHost(t *testing.T) {
	sub, err := nodeSubstrateFor(Config{Substrate: SubstrateMarket})
	if err != nil {
		t.Fatalf("nodeSubstrateFor(market): %v -- a market node is built by the bidder that wins "+
			"its RFP out of cloud-init, so this host needs no VMM, no agent, no image, no pasta "+
			"and no /dev/kvm to place one", err)
	}
	if sub.Name != SubstrateMarket {
		t.Fatalf("nodeSubstrateFor built substrate %q", sub.Name)
	}
	if sub.IsGuest() {
		t.Fatal("a market node was treated as a guest: it is not addressed inside this host's " +
			"subnet lease and it reaches its apiserver through the market's SSH door, so a guest " +
			"gateway and a per-node range would name a network it is not on")
	}
	if err := sub.Validate(); err != nil {
		t.Fatalf("the market substrate refused a whole placement: %v", err)
	}
	expiry := sub.NodeExpiry()
	if expiry == nil {
		t.Fatal("a market node carries no nodeExpiry, and an ABSENT one refuses teardown for ever " +
			"(invariant 9): the door holds the owner's session for the node's whole life, so there " +
			"is no deadline a report could name, and the explicit 0 that means UNLIMITED is the " +
			"only honest value")
	}
	if *expiry != clusterprovider.NodeExpiryUnlimited {
		t.Fatalf("a market node's expiry is %d, want the explicit %d that means unlimited",
			*expiry, clusterprovider.NodeExpiryUnlimited)
	}
}

func TestTheCredentialSourceFollowsTheSubstrate(t *testing.T) {
	for _, tc := range []struct {
		substrate nodeSubstrate
		want      clusterprovider.CredentialSource
	}{
		{nodeSubstrate{Name: SubstrateMarket}, clusterprovider.CredentialSourceClusterCA},
		{nodeSubstrate{Name: SubstrateFirecracker}, clusterprovider.CredentialSourceVaultIfSealed},
		{nodeSubstrate{Name: SubstrateQEMU}, clusterprovider.CredentialSourceVaultIfSealed},
	} {
		t.Run(tc.substrate.Name, func(t *testing.T) {
			got := tenantCredentialSource(tc.substrate)
			if got != tc.want {
				t.Fatalf("a cluster placed on the %s substrate mints its tenant credential as %q, "+
					"want %q: a market node's machine is not this host, so the cluster's own "+
					"ClientCA signs it and the vault is never on the path between this provider "+
					"and the tenant apiserver", tc.substrate.Name, got, tc.want)
			}
		})
	}
}

func TestTheMarketSubstrateBuildsAClaimingHost(t *testing.T) {
	claims := &marketClaims{}
	sub := nodeSubstrate{Name: SubstrateMarket}
	host, err := sub.NewNodeHost(nodeSubstrateOptions{
		Cluster:          "liveweb",
		NodeName:         "liveweb-node1",
		Server:           "https://100.127.255.254:24000",
		TokenFile:        filepath.Join(t.TempDir(), "token"),
		ServerCAFile:     filepath.Join(t.TempDir(), "server-ca.crt"),
		DataDir:          t.TempDir(),
		AdvertiseAddress: portallocator.MarketAdvertiseAddress,
		ApiserverPort:    24000,
		Claims:           claims,
		ClusterSource: func(context.Context) (nodemarket.Cluster, error) {
			return nodemarket.Cluster{Workspace: "root:acct:home", OwnerDid: "did:web:example.com"}, nil
		},
		Record: func(context.Context, string, v1alpha1.NodePhase) error { return nil },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("the market substrate built no node host: %v", err)
	}
	if _, ok := host.(*nodemarket.Host); !ok {
		t.Fatalf("the market substrate built %T, want a claim-writing host", host)
	}
}

func TestTheMarketSubstrateRefusesAHostWithNoStoreToClaimThrough(t *testing.T) {
	hosts, err := newNodeHosts(nodeHostsOptions{
		Allocator:        testAllocator(t),
		Networks:         testNetworks(t),
		Registry:         marketRegistry(),
		StateDir:         t.TempDir(),
		AdvertiseAddress: portallocator.MarketAdvertiseAddress,
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		Substrate:        nodeSubstrate{Name: SubstrateMarket},
	})
	if err == nil {
		t.Fatal("a market placement was accepted with nothing to write a NodeClaim through: the " +
			"gateway would count a node it never asked anybody to rent, and the scale decision " +
			"would ask again on every pass for ever")
	}
	if hosts != nil {
		t.Fatal("a factory was returned alongside the refusal")
	}
}

func TestTwoScalePassesOverAPendingClaimAskForOneNode(t *testing.T) {
	claims := &marketClaims{}
	tenant, _ := intentTenant(t)
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	observer := clusterprovider.NodeObserver{Client: tenant}
	driver, err := scaledriver.New(hosts.forCluster, scaledriver.Options{
		Observe: func(ctx context.Context, cluster string) (scale.Observation, int, error) {
			observed, nodes, err := observer.Observe(ctx, cluster)
			if err != nil {
				return observed, nodes, err
			}
			return observed, nodes + hosts.inFlight(ctx, cluster), nil
		},
		Scale: scale.Options{MaxNodes: 1, PodsPerNode: 1},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("scaledriver.New: %v", err)
	}
	ctx := context.Background()
	for pass := 1; pass <= 2; pass++ {
		if _, err := driver.RunPass(ctx, clusterKey(t, "liveweb")); err != nil {
			t.Fatalf("scale pass %d: %v", pass, err)
		}
	}
	if got := claims.held(); got != 1 {
		t.Fatalf("two passes over a claim the door has not answered left %d claims, want one. The "+
			"pass runs every 2 s and a market node takes minutes, so the nodes a pass observes "+
			"have to include the ones already in flight -- a tenant node list alone reads zero "+
			"for the whole time the market is working", got)
	}
}

func TestOneScalePassClaimsEveryNodeTheTargetNeeds(t *testing.T) {
	claims := &marketClaims{}
	tenant, client := tenantClientFor(t)
	for _, name := range []string{"work-a", "work-b", "work-c"} {
		if _, err := client.CoreV1().Pods("default").Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding the pod %s the scale decision reads as intent: %v", name, err)
		}
	}
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	observer := clusterprovider.NodeObserver{Client: tenant}
	driver, err := scaledriver.New(hosts.forCluster, scaledriver.Options{
		Observe: func(ctx context.Context, cluster string) (scale.Observation, int, error) {
			observed, nodes, err := observer.Observe(ctx, cluster)
			if err != nil {
				return observed, nodes, err
			}
			return observed, nodes + hosts.inFlight(ctx, cluster), nil
		},
		Scale: scale.Options{MaxNodes: 3, PodsPerNode: 1},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("scaledriver.New: %v", err)
	}
	ctx := context.Background()
	if _, err := driver.RunPass(ctx, clusterKey(t, "liveweb")); err != nil {
		t.Fatalf("scale pass: %v", err)
	}
	if got := claims.held(); got != 3 {
		t.Fatalf("one pass that needs three nodes left %d claims, want three. A market claim "+
			"returns before its machine joins, so the tenant cluster's own node list cannot name "+
			"the next ordinal: every provision in the pass then re-asks for the one node in "+
			"flight, and a cluster whose pods need three nodes never asks for more than one", got)
	}
	key := clusterKey(t, "liveweb")
	for _, suffix := range []string{"-node1", "-node2", "-node3"} {
		if _, ok := claims.claims[testHomePrefix+"liveweb/"+key+suffix]; !ok {
			t.Fatalf("the pass left no claim named %s%s, so the nodes it counted are not the nodes "+
				"it asked the market for", key, suffix)
		}
	}
	writes := claims.writes
	if _, err := driver.RunPass(ctx, clusterKey(t, "liveweb")); err != nil {
		t.Fatalf("second scale pass: %v", err)
	}
	if claims.writes != writes {
		t.Fatalf("a second pass rewrote the claims it had already written, %d writes becoming %d. "+
			"The pass runs every 2 s and a market machine takes minutes, so a pass that cannot see "+
			"what it already asked for spends that time asking again, and the writes are what "+
			"throttle the door's own list of the same claims", writes, claims.writes)
	}
}

func TestAMarketNodeIsCountedUntilItsOwnClusterListsIt(t *testing.T) {
	claims := &marketClaims{}
	tenant, client := intentTenant(t)
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	key := clusterKey(t, "liveweb")
	nodeName := key + "-node1"
	if got := hosts.inFlight(ctx, key); got != 0 {
		t.Fatalf("a cluster with no node host counted %d nodes in flight", got)
	}
	nodes, err := hosts.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := hosts.inFlight(ctx, key); got != 1 {
		t.Fatalf("a claim still Pending counted %d nodes in flight, want 1: the cluster's own node "+
			"list does not name it, so nothing else counts it and a pass would ask for another node "+
			"on every tick while this one is still coming up", got)
	}
	claims.setPhase(testHomePrefix+"liveweb", nodeName, v1alpha1.NodeClaimRunning)
	if got := hosts.inFlight(ctx, key); got != 1 {
		t.Fatalf("a claim that reached Running while its cluster still did not list the node "+
			"counted %d nodes in flight, want 1. The door writes Running when the node's agent "+
			"reaches the apiserver through the forward, and the Node object appears in the "+
			"cluster's own node list after that: they are two moments, and a count that ends at the "+
			"first one names a node in NEITHER list for the seconds between them, so every claim "+
			"that crosses together reads as zero nodes and the pass rents a whole second set",
			got)
	}
	if _, err := client.CoreV1().Nodes().Create(ctx, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding the Node the cluster registers once the agent has joined: %v", err)
	}
	if got := hosts.inFlight(ctx, key); got != 0 {
		t.Fatalf("a claim whose node the cluster lists counted %d nodes in flight, want 0: the "+
			"tenant node list already counts it, and counting it twice makes a decision read two "+
			"nodes for one", got)
	}
}

func TestASecondPassRentsNothingWhileTheFirstNodesAreStillJoining(t *testing.T) {
	claims := &marketClaims{}
	tenant, client := intentTenant(t)
	for _, name := range []string{"work-b", "work-c"} {
		if _, err := client.CoreV1().Pods("default").Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding the pod %s the scale decision reads as intent: %v", name, err)
		}
	}
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	observer := clusterprovider.NodeObserver{Client: tenant}
	driver, err := scaledriver.New(hosts.forCluster, scaledriver.Options{
		Observe: func(ctx context.Context, cluster string) (scale.Observation, int, error) {
			observed, nodes, err := observer.Observe(ctx, cluster)
			if err != nil {
				return observed, nodes, err
			}
			return observed, nodes + hosts.inFlight(ctx, cluster), nil
		},
		Scale: scale.Options{MaxNodes: 3, PodsPerNode: 1},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("scaledriver.New: %v", err)
	}
	ctx := context.Background()
	key := clusterKey(t, "liveweb")
	if _, err := driver.RunPass(ctx, key); err != nil {
		t.Fatalf("first scale pass: %v", err)
	}
	if got := claims.held(); got != 3 {
		t.Fatalf("the first pass left %d claims, want three", got)
	}
	for _, suffix := range []string{"-node1", "-node2", "-node3"} {
		claims.setPhase(testHomePrefix+"liveweb", key+suffix, v1alpha1.NodeClaimRunning)
	}
	if _, err := driver.RunPass(ctx, key); err != nil {
		t.Fatalf("second scale pass: %v", err)
	}
	if got := claims.held(); got != 3 {
		t.Fatalf("a second pass over three claims that had all reached Running and whose nodes the "+
			"cluster did not list yet left %d claims, want three. MEASURED in production on "+
			"2026-10-06: the count reached 15 for a target of 10, because a claim that has reached "+
			"Running is in no list at all until its k3s agent registers -- the claim's own phase has "+
			"stopped counting it and the tenant node list does not name it yet -- so the pass read "+
			"zero nodes and rented the whole target again", got)
	}
}

func TestTheMarketTeardownIsTheClaimAndNotTheMachine(t *testing.T) {
	claims := &marketClaims{}
	tenant, _ := tenantClientFor(t)
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
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
	if err := nodes.Teardown(ctx); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if got := claims.deletes; got != 1 {
		t.Fatalf("the teardown issued %d claim deletes, want exactly one: the clusterprovider "+
			"closes the claim and the door destroys the machine, so the two are different acts "+
			"(invariant 18)", got)
	}
	if got := claims.held(); got != 0 {
		t.Fatalf("%d claims survived the teardown", got)
	}
}

func TestTheMarketSweepReapsNoProcessBecauseThereIsNone(t *testing.T) {
	stateDir := t.TempDir()
	nodesDir := filepath.Join(stateDir, "pdr-liveweb", clusterprovider.NodesDirName, "liveweb-node1")
	if err := os.MkdirAll(nodesDir, 0o700); err != nil {
		t.Fatalf("creating a node record dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nodesDir, "node.json"), []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatalf("writing a stale record: %v", err)
	}
	p := &Provider{cfg: Config{Substrate: SubstrateMarket, StateDir: stateDir}, log: testLogger()}
	if err := p.sweepNodeRecords(context.Background()); err != nil {
		t.Fatalf("the market substrate's startup sweep refused: %v -- a market node's machine is "+
			"not a process on this host, so the sweep has nothing to derive an identity from and "+
			"must say so rather than stop a gateway nothing could configure into the market", err)
	}
}

func TestTheMarketRefusesTheLocalPlacementSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  map[string]string
		want []string
	}{
		{"a firecracker binary", map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER": "/usr/bin/firecracker"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER", "/usr/bin/firecracker"}},
		{"a qemu binary", map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_QEMU": "/usr/bin/qemu-system-x86_64"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_QEMU"}},
		{"a node agent", map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT": "/usr/bin/socialweb-nodeagent"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT"}},
		{"an image store", map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR": "/var/lib/images"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR"}},
		{"pasta", map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_NETWORK": "pasta"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_NETWORK"}},
		{"the guest advertise address", map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS": "192.168.5.2"},
			[]string{"SOCIALWEB_CLUSTERPROVIDER_ADVERTISE_ADDRESS", "192.168.5.2",
				"SOCIALWEB_CLUSTERPROVIDER_MARKET_ADVERTISE_ADDRESS"}},
	} {
		_, err := loadJoin(t, marketEnv(t, tc.set))
		if err == nil {
			t.Errorf("%s was accepted under the market substrate, and it cannot be in force there: "+
				"an operator who set it and saw no complaint would believe it was carrying it", tc.name)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q: %v", tc.name, want, err)
			}
		}
	}
}

func TestAMarketJoinNeedsOnlyTheMarketAdvertiseAddress(t *testing.T) {
	cfg, err := loadJoin(t, marketEnv(t, nil))
	if err != nil {
		t.Fatalf("a fully configured market join was refused: %v -- a market node needs no VMM, "+
			"no agent, no image, no pasta and no /dev/kvm on this host", err)
	}
	if cfg.Substrate != SubstrateMarket {
		t.Fatalf("the market placement was not carried into the Config: %+v", cfg)
	}
	if cfg.AdvertiseAddress != portallocator.MarketAdvertiseAddress {
		t.Fatalf("the k3s server's --advertise-address is %q, want the market's %q: the guest puts "+
			"that address on its OWN loopback and the door reverse-forwards it to the apiserver",
			cfg.AdvertiseAddress, portallocator.MarketAdvertiseAddress)
	}
	if cfg.MarketAdvertiseAddress != portallocator.MarketAdvertiseAddress {
		t.Fatalf("the market advertise address defaulted to %q, want %q",
			cfg.MarketAdvertiseAddress, portallocator.MarketAdvertiseAddress)
	}
}

func TestAMarketAdvertiseAddressThisHostCarriesIsRefused(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "0.0.0.0", "not-an-address"} {
		_, err := loadJoin(t, marketEnv(t, map[string]string{
			"SOCIALWEB_CLUSTERPROVIDER_MARKET_ADVERTISE_ADDRESS": address,
		}))
		if err == nil {
			t.Errorf("%q was accepted as the address a market node dials its apiserver on: the "+
				"guest puts it on its own loopback and the door forwards it here, so an address "+
				"this host already holds is one the forward collides with", address)
			continue
		}
		if !strings.Contains(err.Error(), "SOCIALWEB_CLUSTERPROVIDER_MARKET_ADVERTISE_ADDRESS") {
			t.Errorf("%q: the refusal does not name the setting: %v", address, err)
		}
	}
}

func marketEnv(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	base := map[string]string{
		"SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE":     SubstrateMarket,
		"SOCIALWEB_CLUSTERPROVIDER_FIRECRACKER":   "",
		"SOCIALWEB_CLUSTERPROVIDER_QEMU":          "",
		"SOCIALWEB_CLUSTERPROVIDER_NODE_AGENT":    "",
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_OUT_DIR": "",
		"SOCIALWEB_CLUSTERPROVIDER_IMAGE_CONFIG":  "",
		"SOCIALWEB_CLUSTERPROVIDER_NETWORK":       "",
	}
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func TestAGuestSubstrateCountsNoNodeInFlight(t *testing.T) {
	tenant, _ := tenantClientFor(t)
	hosts, err := newNodeHosts(nodeOptionsFor(t, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	key := "liveweb"
	if _, err := hosts.forCluster(key); err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if got := hosts.inFlight(context.Background(), key); got != 0 {
		t.Fatalf("a firecracker host was counted as %d nodes in flight. A guest substrate's Provision "+
			"does not return until the tenant cluster reports the node, so a host it still holds and "+
			"the tenant does not name is a join that ran out its timeout, not a machine on its way: "+
			"counting it holds the scale decision off a node that is never coming, and it is exactly "+
			"the count the market substrate needs and this one must not have", got)
	}
}

func TestANodeNameWhoseClaimHasEndedIsNotClaimedAgain(t *testing.T) {
	claims := &marketClaims{}
	tenant, client := intentTenant(t)
	for _, name := range []string{"work-b"} {
		if _, err := client.CoreV1().Pods("default").Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding the pod %s: %v", name, err)
		}
	}
	key := clusterKey(t, "liveweb")
	ended := testHomePrefix + "liveweb/" + key + "-node2"
	claims.claims = map[string]v1alpha1.NodeClaim{
		ended: {
			ObjectMeta: metav1.ObjectMeta{Name: key + "-node2"},
			Status: v1alpha1.NodeClaimStatus{
				Phase:   v1alpha1.NodeClaimFailed,
				Message: "the door restarted and released this node's VM; a new node will be requested",
			},
		},
	}
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	nodes, err := hosts.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if claim, ok := claims.claims[ended]; !ok || claim.Status.Phase != v1alpha1.NodeClaimFailed {
		t.Fatal("the test's own ended claim was overwritten, so nothing here says what a second " +
			"provision does with a spent name")
	}
	if _, ok := claims.claims[testHomePrefix+"liveweb/"+key+"-node3"]; !ok {
		t.Fatalf("the second provision did not name node3, so it asked the market for a name whose "+
			"claim has already ended. MEASURED in production on 2026-10-06: a node claimed 2 s after "+
			"it was written was torn down on the next pass, because WriteNodeClaimTo never touches "+
			"status and a claim whose SPEC matches is left exactly as it was -- Failed, with the door's "+
			"old words still on it -- so the provider read its own fresh claim as a failure. The names "+
			"in the workspace were: %v", keysOf(claims.claims))
	}
}

func keysOf(m map[string]v1alpha1.NodeClaim) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestANodeThatIsTornDownStopsBeingCounted(t *testing.T) {
	claims := &marketClaims{}
	tenant, _ := intentTenant(t)
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
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
	if got := hosts.inFlight(ctx, key); got != 1 {
		t.Fatalf("a live claim counted %d nodes in flight, want 1", got)
	}
	claims.setPhase(testHomePrefix+"liveweb", key+"-node1", v1alpha1.NodeClaimFailed)
	err = hosts.confirm(ctx, key)
	if err == nil || !strings.Contains(err.Error(), "stopped making progress") {
		t.Fatalf("confirming a node whose claim failed reported %v, want the failure itself: the "+
			"pass returns it so a failed node is visible rather than silent", err)
	}
	if got := claims.held(); got != 0 {
		t.Fatalf("the failed node's claim is still there after its teardown, %d claims", got)
	}
	if got := hosts.inFlight(ctx, key); got != 0 {
		t.Fatalf("a node that was torn down is still counted as %d nodes in flight. MEASURED in "+
			"production on 2026-10-06: 11 claims had been written and 7 released, leaving 4 live "+
			"claims and 4 Nodes, and the count read 11 -- so the cluster could not scale up again, "+
			"because the ceiling it had already passed is what stops a provision. The count is the "+
			"tenant cluster's node list plus the claims this process holds, so a host that is torn "+
			"down has to stop being held: its claim is gone, its Node is deleted, and a hold that "+
			"outlives both is a node that is counted for ever", got)
	}
}

func TestTheFirstNodeOfAClusterIsNotClaimedUnderASpentName(t *testing.T) {
	claims := &marketClaims{}
	tenant, _ := intentTenant(t)
	key := clusterKey(t, "liveweb")
	spent := testHomePrefix + "liveweb/" + key + "-node1"
	claims.claims = map[string]v1alpha1.NodeClaim{
		spent: {
			ObjectMeta: metav1.ObjectMeta{Name: key + "-node1"},
			Status: v1alpha1.NodeClaimStatus{
				Phase:   v1alpha1.NodeClaimFailed,
				Message: "the door restarted and released this node's VM; a new node will be requested",
			},
		},
	}
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	nodes, err := hosts.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if claim, ok := claims.claims[spent]; !ok || claim.Status.Phase != v1alpha1.NodeClaimFailed {
		t.Fatal("the spent claim's name was taken, so the first node of this cluster was claimed " +
			"under a name whose claim has already ended")
	}
	if _, ok := claims.claims[testHomePrefix+"liveweb/"+key+"-node2"]; !ok {
		t.Fatalf("the first provision did not name node2. MEASURED in production on 2026-10-06: after "+
			"a deploy restarted the door, the provider built its first node as node1 at construction "+
			"and Provision took ordinal 1 straight to the host already in hand, never consulting the "+
			"free-ordinal rule -- so it re-claimed node1, and kcpclient.WriteNodeClaimTo leaves a claim "+
			"whose spec matches exactly as it was, Failed status and the door's old words included. "+
			"The provider then read its own fresh claim as a failure and released it: node1 claimed at "+
			"06:18:38 and released at 06:18:41. The names now are: %v", keysOf(claims.claims))
	}
	if got := hosts.inFlight(ctx, key); got != 1 {
		t.Fatalf("the cluster holds %d nodes in flight, want 1: the host built for node1 was dropped "+
			"when its name turned out to be spent, and the one that replaced it is the only one it "+
			"holds", got)
	}
}

func TestANodeWhoseClaimTheDoorConfirmedReleasedIsRemoved(t *testing.T) {
	claims := &marketClaims{}
	key := clusterKey(t, "liveweb")
	gone := key + "-node1"
	live := key + "-node2"
	tenant, client := tenantClientFor(t, gone, live)
	claims.claims = map[string]v1alpha1.NodeClaim{
		testHomePrefix + "liveweb/" + gone: {
			ObjectMeta: metav1.ObjectMeta{Name: gone},
			Status: v1alpha1.NodeClaimStatus{
				Phase:   v1alpha1.NodeClaimReleased,
				Message: "the door restarted and released this node's VM; a new node will be requested",
			},
		},
		testHomePrefix + "liveweb/" + live: {
			ObjectMeta: metav1.ObjectMeta{Name: live},
			Status:     v1alpha1.NodeClaimStatus{Phase: v1alpha1.NodeClaimRunning},
		},
	}
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	if err := hosts.reconcile(ctx, key); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if tenantNode(t, client, gone) {
		t.Fatalf("the Node %s is still in the tenant cluster after the door confirmed its machine "+
			"gone. MEASURED in production on 2026-10-06: forty seconds after a deploy restarted the "+
			"door, pipeline 74's four runner pods were scheduled onto just such a Node, sat Pending "+
			"for 58 s and then vanished when the cloud controller manager deleted it -- four of five "+
			"jobs failed that way", gone)
	}
	if !tenantNode(t, client, live) {
		t.Fatalf("the Node %s was removed while its claim reads Running, so a node that is serving "+
			"was deleted", live)
	}
}

func TestANodeIsNotRemovedWhileItsClaimIsStillRunning(t *testing.T) {
	claims := &marketClaims{}
	key := clusterKey(t, "liveweb")
	name := key + "-node1"
	tenant, client := tenantClientFor(t, name)
	claims.claims = map[string]v1alpha1.NodeClaim{
		testHomePrefix + "liveweb/" + name: {
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status:     v1alpha1.NodeClaimStatus{Phase: v1alpha1.NodeClaimRunning},
		},
	}
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	if err := hosts.reconcile(context.Background(), key); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !tenantNode(t, client, name) {
		t.Fatal("a Node whose claim is still Running was deleted by the reconcile")
	}
}
