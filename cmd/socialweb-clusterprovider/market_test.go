//go:build linux

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

func TestAMarketNodeIsCountedUntilTheDoorReportsItRunning(t *testing.T) {
	claims := &marketClaims{}
	tenant, _ := tenantClientFor(t)
	hosts, err := newNodeHosts(marketOptions(t, claims, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	ctx := context.Background()
	if got := hosts.inFlight(ctx, clusterKey(t, "liveweb")); got != 0 {
		t.Fatalf("a cluster with no node host counted %d nodes in flight", got)
	}
	nodes, err := hosts.forCluster(clusterKey(t, "liveweb"))
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := hosts.inFlight(ctx, clusterKey(t, "liveweb")); got != 1 {
		t.Fatalf("a claim still Pending counted %d nodes in flight, want 1", got)
	}
	claims.setPhase(testHomePrefix+"liveweb", clusterKey(t, "liveweb")+"-node1", v1alpha1.NodeClaimRunning)
	if got := hosts.inFlight(ctx, clusterKey(t, "liveweb")); got != 0 {
		t.Fatalf("a Running claim counted %d nodes in flight, want 0: the node it names is in its "+
			"cluster's own node list, and counting it twice makes a decision read two nodes for one",
			got)
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
