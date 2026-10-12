//go:build linux

package providermain

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const standInEnv = "CLUSTERPROVIDER_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(standInEnv) == "" {
		os.Exit(m.Run())
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	os.Exit(0)
}

type fakeNodeHost struct {
	mu         sync.Mutex
	provisions int
	teardowns  int
	confirmed  int
	err        error

	seq *[]string
}

func (f *fakeNodeHost) Provision(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisions++
	f.note("host-provision")
	return f.err
}

func (f *fakeNodeHost) ConfirmJoined(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmed++
	return f.err
}

func (f *fakeNodeHost) Teardown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardowns++
	f.note("host-teardown")
	return f.err
}

func (f *fakeNodeHost) Reap(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note("host-reap")
	return f.err
}

func (f *fakeNodeHost) note(what string) {
	if f.seq != nil {
		*f.seq = append(*f.seq, what)
	}
}

func (f *fakeNodeHost) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.provisions, f.teardowns
}

type nodeWritingClusters struct {
	fakeClusters
	mu       sync.Mutex
	nodes    []string
	statuses []v1alpha1.NodeStatus
}

func (c *nodeWritingClusters) WriteNode(_ context.Context, _ clusterprovider.Ref, nodeName string, status v1alpha1.NodeStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = append(c.nodes, nodeName)
	c.statuses = append(c.statuses, status)
	return nil
}

func (c *nodeWritingClusters) written() ([]string, []v1alpha1.NodePhase) {
	names, statuses := c.wrote()
	phases := make([]v1alpha1.NodePhase, 0, len(statuses))
	for _, st := range statuses {
		phases = append(phases, st.Phase)
	}
	return names, phases
}

func (c *nodeWritingClusters) wrote() ([]string, []v1alpha1.NodeStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.nodes...), append([]v1alpha1.NodeStatus(nil), c.statuses...)
}

func tenantClientFor(t *testing.T, nodes ...string) (tenantClient, *fake.Clientset) {
	t.Helper()
	items := make([]*corev1.Node, 0, len(nodes))
	for _, name := range nodes {
		items = append(items, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	client := fake.NewClientset()
	for _, item := range items {
		if _, err := client.CoreV1().Nodes().Create(context.Background(), item, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding node %s: %v", item.Name, err)
		}
	}
	return func(context.Context, string) (kubernetes.Interface, error) { return client, nil }, client
}

func tenantNode(t *testing.T, client kubernetes.Interface, name string) bool {
	t.Helper()
	_, err := client.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatalf("reading node %s: %v", name, err)
	}
	return true
}

func TestTheNodeHostsAreOneHostPerCluster(t *testing.T) {
	factory, err := newNodeHosts(marketOptions(t, &marketClaims{}, nil))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	first, err := factory.forCluster("liveweb")
	if err != nil {
		t.Fatalf("factory.forCluster(liveweb): %v", err)
	}
	again, err := factory.forCluster("liveweb")
	if err != nil {
		t.Fatalf("factory.forCluster(liveweb) again: %v", err)
	}
	if first != again {
		t.Fatalf("two calls for liveweb returned %p and %p: a Provision in one pass and the "+
			"Teardown in a later pass must act on the SAME host, or the teardown finds no process "+
			"handle and a node that was started can never be stopped, so the cluster never scales "+
			"to zero", first, again)
	}
	other, err := factory.forCluster("secondweb")
	if err != nil {
		t.Fatalf("factory.forCluster(secondweb): %v", err)
	}
	if other == first {
		t.Fatalf("two clusters were handed the same host: one host carries one node name and one " +
			"VMM, so the second cluster's node would be the first cluster's node")
	}
	if a, b := first.(*tenantNodes).host, other.(*tenantNodes).host; a == b {
		t.Fatal("two clusters share one node host, and one host carries one node record, one guest " +
			"and one node name")
	}
	if name := other.(*tenantNodes).nodeName; name != "secondweb-node1" {
		t.Fatalf("the second cluster's node is named %q", name)
	}
	if got := factory.clusters(); len(got) != 2 || got[0] != "liveweb" || got[1] != "secondweb" {
		t.Fatalf("the handle lists clusters %v, want the two it holds: the shutdown reap walks "+
			"exactly this list, so a cluster missing from it is a node host nothing reaps", got)
	}
}

func TestTheNodeHostsAreSafeToBuildConcurrently(t *testing.T) {
	factory, err := newNodeHosts(marketOptions(t, &marketClaims{}, nil))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	const builders = 16
	got := make([]scale.Nodes, builders)
	var wg sync.WaitGroup
	for i := 0; i < builders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, err := factory.forCluster("liveweb")
			if err != nil {
				t.Errorf("forCluster: %v", err)
				return
			}
			got[i] = n
		}(i)
	}
	wg.Wait()
	for i := 1; i < builders; i++ {
		if got[i] != got[0] {
			t.Fatalf("concurrent builds returned %p and %p: a second host for one cluster is a "+
				"second guest under one node name", got[i], got[0])
		}
	}
}

func TestTheNodeTeardownDeletesTheTenantsNodeObject(t *testing.T) {
	tenant, client := tenantClientFor(t, "liveweb-node1")
	host := &fakeNodeHost{}
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1", host: host, tenant: tenant,
		log: testLogger(), opts: nodeHostsOptions{StateDir: t.TempDir()},
	}
	if err := n.Teardown(context.Background()); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, teardowns := host.calls(); teardowns != 1 {
		t.Fatalf("the node's own teardown ran %d times", teardowns)
	}
	if tenantNode(t, client, "liveweb-node1") {
		t.Fatal("the tenant cluster still counts a node whose agent is stopped: a k3s agent does " +
			"not delete its own Node object when it stops, so the scale decision reads a node with " +
			"no intent on every pass and the cluster never reaches zero")
	}
}

func TestTheNodeTeardownToleratesANodeObjectThatIsAlreadyGone(t *testing.T) {
	tenant, _ := tenantClientFor(t)
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1", host: &fakeNodeHost{},
		tenant: tenant, log: testLogger(), opts: nodeHostsOptions{StateDir: t.TempDir()},
	}
	if err := n.Teardown(context.Background()); err != nil {
		t.Fatalf("Teardown with no Node object to delete = %v, want nil: the object is the thing "+
			"this call removes, and a teardown that fails because it is already absent is a "+
			"cluster that can never reach zero nodes", err)
	}
}

func TestTheNodeProvisionDeletesNothing(t *testing.T) {
	tenant, client := tenantClientFor(t, "liveweb-node1")
	host := &fakeNodeHost{}
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1", host: host, tenant: tenant,
		log: testLogger(), opts: nodeHostsOptions{StateDir: t.TempDir()},
	}
	if err := n.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if provisions, _ := host.calls(); provisions != 1 {
		t.Fatalf("the node's own provision ran %d times", provisions)
	}
	if !tenantNode(t, client, "liveweb-node1") {
		t.Fatal("a provision removed the tenant cluster's node object: the node is what the " +
			"provision is bringing up, and deleting its object would hide it from the count that " +
			"decides whether to provision again")
	}
}

func TestTheNodeTeardownRefusesWithoutATenantClient(t *testing.T) {
	host := &fakeNodeHost{}
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1", host: host,
		log: testLogger(),
	}
	err := n.Teardown(context.Background())
	if err == nil {
		t.Fatal("a teardown with no tenant client was reported as success: the Node object it " +
			"cannot delete leaves the tenant cluster counting a node that is gone, and a skipped " +
			"cleanup and a completed one look identical from a green suite")
	}
	if !errors.Is(err, errNoTenantClient) {
		t.Fatalf("Teardown = %v, want the refusal that names what a missing client prevents", err)
	}
	if _, teardowns := host.calls(); teardowns != 0 {
		t.Fatal("the agent was stopped before the client was checked: a stopped agent behind a " +
			"Node object nothing can delete is a count that can never reach zero")
	}
}

func TestTheNodeTeardownRefusesWhenTheTenantClientCannotBeBuilt(t *testing.T) {
	cause := errors.New("the tenant credential has no process-substrate implementation")
	tenant := func(context.Context, string) (kubernetes.Interface, error) { return nil, cause }
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1", host: &fakeNodeHost{},
		tenant: tenant, log: testLogger(), opts: nodeHostsOptions{StateDir: t.TempDir()},
	}
	if err := n.Teardown(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("Teardown = %v, want the client's own failure wrapped", err)
	}
}

func TestNodeReportedAnswersFromTheTenantCluster(t *testing.T) {
	tenant, _ := tenantClientFor(t, "liveweb-node1")
	ctx := context.Background()
	reported, err := nodeReported(ctx, tenant, "liveweb", "liveweb-node1")
	if err != nil {
		t.Fatalf("nodeReported: %v", err)
	}
	if !reported {
		t.Fatal("a node the tenant cluster lists was reported as missing, so Pending could never " +
			"become Running")
	}
	reported, err = nodeReported(ctx, tenant, "liveweb", "liveweb-node2")
	if err != nil {
		t.Fatalf("nodeReported: %v", err)
	}
	if reported {
		t.Fatal("a node the tenant cluster does not list was reported as present, so a node would " +
			"be promoted to Running on nothing")
	}
	if _, err := nodeReported(ctx, nil, "liveweb", "liveweb-node1"); !errors.Is(err, errNoJoiningClient) {
		t.Fatalf("nodeReported with no client = %v, want the refusal that names what a missing "+
			"client prevents", err)
	}
}

func TestTheNodeRecordGoesThroughTheRegistryAndNamesTheNode(t *testing.T) {
	registry := &nodeWritingClusters{fakeClusters: fakeClusters{items: []v1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: "liveweb"},
		Spec:       v1alpha1.ClusterSpec{Account: "did:web:example.com"},
	}}}}
	expiry := int64(1790000000)
	rec := nodeRecorder(registry, clusterKey(t, "liveweb"), "liveweb-node1", &expiry)
	if err := rec(context.Background(), "liveweb-node1", v1alpha1.NodePending); err != nil {
		t.Fatalf("record(Pending): %v", err)
	}
	if err := rec(context.Background(), "liveweb-node1", v1alpha1.NodeRunning); err != nil {
		t.Fatalf("record(Running): %v", err)
	}
	names, phases := registry.written()
	if len(names) != 2 || names[0] != "liveweb-node1" || names[1] != "liveweb-node1" {
		t.Fatalf("written names = %v, want the (cluster, node) name twice", names)
	}
	if phases[0] != v1alpha1.NodePending || phases[1] != v1alpha1.NodeRunning {
		t.Fatalf("written phases = %v, want Pending then Running", phases)
	}
	_, statuses := registry.wrote()
	for i, st := range statuses {
		if st.NodeExpiry == nil || *st.NodeExpiry != expiry {
			t.Fatalf("the write %d carries nodeExpiry %v, want the node's own report %d: the "+
				"Cluster's nodeExpiry is a roll-up OVER these, so a node that reports nothing "+
				"leaves the cluster with no value to roll up", i, st.NodeExpiry, expiry)
		}
	}
	missing := nodeRecorder(registry, "otherweb", "otherweb-node1", &expiry)
	if err := missing(context.Background(), "otherweb-node1", v1alpha1.NodeRunning); err == nil {
		t.Fatal("a node phase was recorded for a cluster the registry does not list: the phase " +
			"would be written to whichever CR happened to match, and the count the scale decision " +
			"reads would belong to another cluster")
	}
}

func TestTheNodeTeardownStopsTheGuestBeforeItDeletesTheNodeObject(t *testing.T) {
	var seq []string
	tenant, client := tenantClientFor(t, "liveweb-node1")
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1",
		host:   &fakeNodeHost{seq: &seq},
		tenant: tenant, log: testLogger(), opts: nodeHostsOptions{StateDir: t.TempDir()},
	}
	if err := n.Teardown(context.Background()); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	want := []string{"host-teardown"}
	if len(seq) != len(want) || seq[0] != want[0] {
		t.Fatalf("the teardown did %v, want %v. A node's teardown gives back no lease: every port "+
			"the guest binds is bound inside its own network namespace, and the cluster's own "+
			"leases outlive the node because status.URL stays live after OFF", seq, want)
	}
	if tenantNode(t, client, "liveweb-node1") {
		t.Fatal("the guest was stopped and the tenant cluster still counts its node object")
	}
}

func TestANodeTeardownThatCannotStopTheGuestLeavesTheNodeObjectAlone(t *testing.T) {
	cause := errors.New("the guest would not stop")
	tenant, client := tenantClientFor(t, "liveweb-node1")
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1",
		host:   &fakeNodeHost{err: cause},
		tenant: tenant, log: testLogger(),
	}
	if err := n.Teardown(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("Teardown = %v, want the host's own failure", err)
	}
	if !tenantNode(t, client, "liveweb-node1") {
		t.Fatal("the node object was deleted although the guest is still up: that object is the " +
			"count the scale decision reads, and removing it while the guest serves hides a node " +
			"that exists")
	}
}

func TestAProvisionThatFailsNamesTheNodeItWasFor(t *testing.T) {
	cause := errors.New("the guest would not boot")
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1",
		host: &fakeNodeHost{err: cause},
		log:  testLogger(),
	}
	err := n.Provision(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("Provision = %v, want the host's own failure wrapped", err)
	}
	for _, want := range []string{"liveweb-node1", "liveweb"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure does not name %q, so an operator with more than one cluster "+
				"cannot tell which node failed to come up: %v", want, err)
		}
	}
}

func TestATeardownOfANodeDoesNotLoseTheClustersHost(t *testing.T) {
	tenant, _ := tenantClientFor(t)
	factory, err := newNodeHosts(marketOptions(t, &marketClaims{}, tenant))
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	key := clusterKey(t, "liveweb")
	first, err := factory.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := first.Teardown(context.Background()); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	again, err := factory.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster after the teardown: %v", err)
	}
	if again != first {
		t.Fatal("the teardown dropped the cluster's host, so the pass that scales the cluster back " +
			"up would provision through a second object while the first one's record, data dir and " +
			"node name are what the tenant cluster's count is keyed on")
	}
}

func TestAJoinSweepsTheNodeRecordsBeforeTheFirstPass(t *testing.T) {
	stateDir := t.TempDir()
	const cluster = "liveweb"
	left := clusterprovider.NodeDataDir(stateDir, cluster, cluster+"-node1")
	pid := spawnAgentStandIn(t, left)
	writeNodeRecordFor(t, left, pid, standInIdentity(left), "firecracker")
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	f := &fakeClusters{items: []v1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: cluster},
		Spec:       v1alpha1.ClusterSpec{Account: "did:web:example.com"},
	}}}
	cfg := Config{
		LeasePath:       filepath.Join(t.TempDir(), "ports.json"),
		GrantPath:       DefaultGrantPath,
		MintKubeconfigs: t.TempDir(),
		Join:            cluster,
		StateDir:        stateDir,
		Substrate:       SubstrateMarket,
		StoreEndpoint:   "postgres://operator@127.0.0.1:25432/k3s?sslmode=disable",
		Interval:        DefaultInterval,
	}
	d := testDeps(t, f)
	d.Host = materialiser.NewLocalHost(procgroup.Options{})
	p, err := Build(cfg, d, testLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = p.Run(ctx)

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the join's pre-pass sweep signalled pid %d (%v). Its record names the retired "+
			"firecracker substrate, whose process identity this provider can no longer reconstruct, "+
			"so nothing may be signalled on it", pid, err)
	}
	if _, err := os.Stat(noderecord.RecordPath(left)); err != nil {
		t.Fatalf("the join's pre-pass sweep cleared a record it cannot verify: %v", err)
	}
}

func TestShutdownLeavesTheMarketNodesToTheNextProvider(t *testing.T) {
	stateDir := t.TempDir()
	opts := marketOptions(t, &marketClaims{}, nil)
	opts.StateDir = stateDir
	factory, err := newNodeHosts(opts)
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	nodes, err := factory.forCluster("liveweb")
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	dir := clusterprovider.NodeDataDir(stateDir, "liveweb", nodes.(*tenantNodes).nodeName)
	pid := spawnAgentStandIn(t, dir)
	writeNodeRecordFor(t, dir, pid, standInIdentity(dir), "firecracker")
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	p := &Provider{
		cfg:   Config{Join: "liveweb"},
		host:  materialiser.NewLocalHost(procgroup.Options{}),
		nodes: factory,
		log:   testLogger(),
	}
	if err := p.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the shutdown signalled pid %d (%v). A market guest joined with no session of "+
			"ours and outlives this process; deleting its claim is what makes the door destroy the "+
			"machine, so a restart that released here would reap every node on every deploy", pid, err)
	}
	if _, err := os.Stat(noderecord.RecordPath(dir)); err != nil {
		t.Fatalf("the shutdown cleared a node record it cannot verify: %v", err)
	}
}

func TestTheSweepLeavesAForeignProcessAlone(t *testing.T) {
	stateDir := t.TempDir()
	dir := clusterprovider.NodeDataDir(stateDir, "liveweb", "liveweb-node1")
	pid := spawnAgentStandIn(t, filepath.Join(t.TempDir(), "otherweb-node9"))
	writeNodeRecordFor(t, dir, pid, standInIdentity(dir), "firecracker")

	p := &Provider{
		cfg: Config{Join: "liveweb", Substrate: SubstrateMarket, StateDir: stateDir},
		log: testLogger(),
	}
	if err := p.sweepNodeRecords(context.Background()); err != nil {
		t.Fatalf("sweepNodeRecords: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the sweep signalled pid %d, whose command line names another node: that process "+
			"belongs to another node's agent, and killing it kills a node that is serving (%v)", pid, err)
	}
	if _, err := os.Stat(noderecord.RecordPath(dir)); err != nil {
		t.Fatalf("the sweep cleared a record that is not ours (%v): the mismatch is what a later "+
			"pass reads, and hiding it is how a node is provisioned over a process nobody accounted for", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}

func TestTheSweepOnAnEmptyStateDirIsANoOp(t *testing.T) {
	stateDir := t.TempDir()
	p := &Provider{
		cfg: Config{Join: "liveweb", Substrate: SubstrateMarket, StateDir: stateDir},
		log: testLogger(),
	}
	if err := p.sweepNodeRecords(context.Background()); err != nil {
		t.Fatalf("sweepNodeRecords on an empty state dir = %v, want nil: the first gateway on a "+
			"host has nothing to reap, and refusing here would wedge every fresh install", err)
	}
	missing := filepath.Join(t.TempDir(), "absent")
	p.cfg.StateDir = missing
	if err := p.sweepNodeRecords(context.Background()); err != nil {
		t.Fatalf("sweepNodeRecords with no state dir at all = %v, want nil", err)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the sweep wrote %d entries into an empty state dir", len(entries))
	}
}

func TestTheSweepReportsARecordItCannotRead(t *testing.T) {
	stateDir := t.TempDir()
	dir := clusterprovider.NodeDataDir(stateDir, "liveweb", "liveweb-node1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(noderecord.RecordPath(dir), []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	p := &Provider{
		cfg: Config{Join: "liveweb", Substrate: SubstrateMarket, StateDir: stateDir},
		log: testLogger(),
	}
	err := p.sweepNodeRecords(context.Background())
	if err == nil {
		t.Fatal("a node record that cannot be read was reported as a successful sweep: the record " +
			"names a pid this gateway would signal, and provisioning over a process nobody can " +
			"account for is how two agents end up under one node name")
	}
	if !strings.Contains(err.Error(), filepath.Base(dir)) {
		t.Errorf("the refusal does not name the node it stopped on: %v", err)
	}
}

func TestNoPlacementSuppliesAProcessIdentityAnyMore(t *testing.T) {
	for _, substrate := range []string{"firecracker", "qemu", "vm", "", "process", SubstrateMarket} {
		if err := unverifiableNodeRecord(substrate); err == nil {
			t.Errorf("a record naming the %q substrate was treated as verifiable: the local "+
				"substrates were retired (plan 0005 M1-MARKET-ONLY) and a market node is not a "+
				"process on this host, so every record under this state dir is one this provider "+
				"cannot check. A guess would either signal a process that is not ours or leave a "+
				"stale one running for ever", substrate)
		}
	}
}

func TestTheProcessSubstrateIsNotASubstrate(t *testing.T) {
	if _, err := nodeSubstrateFor(Config{Substrate: "process"}); err == nil {
		t.Fatal("the process substrate was still built: a k3s agent on this host is fatal as a " +
			"non-root user and postgres is fatal AS root, so a host that runs the cluster's own " +
			"store cannot run a node, and a gateway that accepted this name would place a node " +
			"nowhere")
	}
	if _, err := nodeSubstrateFor(Config{}); !errors.Is(err, errNoSubstrateChoice) {
		t.Fatalf("an unchosen substrate = %v, want the refusal that says no placement was named", err)
	}
}

func TestTheRetiredSubstratesAreRefusedAndNameTheirRetirement(t *testing.T) {
	for _, name := range []string{"firecracker", "qemu"} {
		_, err := nodeSubstrateFor(Config{Substrate: name})
		if err == nil {
			t.Fatalf("%s was still built as a placement: the local substrates were retired on %s "+
				"(plan 0005 M1-MARKET-ONLY), and a gateway that accepted the name would start "+
				"looking for a VMM, an agent and an image this host is no longer given",
				name, RetiredSubstratesRetiredOn)
			continue
		}
		for _, want := range []string{name, SubstrateMarket, RetiredSubstratesRetiredOn} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q, so it reads as a typo rather than as a "+
					"placement that was withdrawn: %v", name, want, err)
			}
		}
	}
}

func TestTheSweepLeavesARecordWhoseSubstrateItCannotDeriveAnIdentityFor(t *testing.T) {
	stateDir := t.TempDir()
	dir := clusterprovider.NodeDataDir(stateDir, "liveweb", "liveweb-node1")
	pid := spawnAgentStandIn(t, dir)
	writeNodeRecordFor(t, dir, pid, standInIdentity(dir), "vm")
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	p := &Provider{
		cfg: Config{Join: "liveweb", Substrate: SubstrateMarket, StateDir: stateDir},
		log: testLogger(),
	}
	if err := p.sweepNodeRecords(context.Background()); err != nil {
		t.Fatalf("sweepNodeRecords: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the sweep signalled pid %d, whose record names a substrate with no identity "+
			"function: guessing would signal a process that is not ours (%v)", pid, err)
	}
	if _, err := os.Stat(noderecord.RecordPath(dir)); err != nil {
		t.Fatalf("the sweep cleared a record whose identity it cannot reconstruct (%v): a cleared "+
			"record takes away the only thing naming the process holding this node", err)
	}
}

func standInIdentity(dataDir string) []string {
	return []string{"--stand-in", dataDir}
}

func spawnAgentStandIn(t *testing.T, dataDir string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Args = append([]string{os.Args[0]}, standInIdentity(dataDir)...)
	cmd.Env = append(os.Environ(), standInEnv+"=1")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a stand-in process for %s: %v", dataDir, err)
	}
	go func() { _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return pid
}

func writeNodeRecordFor(t *testing.T, dir string, pid int, identity []string, substrate string) {
	t.Helper()
	start, err := noderecord.ProcStart(pid)
	if err != nil {
		t.Fatalf("ProcStat(%d): %v", pid, err)
	}
	if err := noderecord.Write(dir, noderecord.Record{
		PID: pid, StartTime: start, Identity: identity, Substrate: substrate,
	}); err != nil {
		t.Fatalf("writing the node record at %s: %v", dir, err)
	}
}

func TestTheNodeIDForAnOrdinalRoundTripsThroughItsName(t *testing.T) {
	if got := nodeIDFor(3); got != "node3" {
		t.Fatalf("nodeIDFor(3) = %q, want node3", got)
	}
	if ordinal, err := nodeOrdinal(nodeIDFor(7)); err != nil || ordinal != 7 {
		t.Fatalf("nodeOrdinal(%q) = %d, %v, want 7", nodeIDFor(7), ordinal, err)
	}
	if ordinal, ok := nodeOrdinalInName("liveweb-node", "liveweb-node3"); !ok || ordinal != 3 {
		t.Fatalf("nodeOrdinalInName(liveweb-node3) = %d, %v, want 3", ordinal, ok)
	}
	for _, bad := range []string{"liveweb-node0", "liveweb-node", "liveweb-nodex", "secondweb-node1", "liveweb-node1-extra"} {
		if ordinal, ok := nodeOrdinalInName("liveweb-node", bad); ok {
			t.Fatalf("nodeOrdinalInName(%q) = %d, want not-ok: another cluster's name or a "+
				"non-ordinal suffix must not be read as this cluster's node", bad, ordinal)
		}
	}
}

func TestTheNextOrdinalComesFromTheTenantClustersOwnNodes(t *testing.T) {
	cases := []struct {
		name  string
		nodes []string
		want  int
	}{
		{"no nodes", nil, 1},
		{"one node", []string{"liveweb-node1"}, 2},
		{"two nodes", []string{"liveweb-node1", "liveweb-node2"}, 3},
		{"a gap is filled", []string{"liveweb-node1", "liveweb-node3"}, 2},
		{"another cluster's nodes are ignored", []string{"secondweb-node1", "secondweb-node2"}, 1},
	}
	for _, tc := range cases {
		tenant, _ := tenantClientFor(t, tc.nodes...)
		n := &tenantNodes{cluster: "liveweb", tenant: tenant, log: testLogger()}
		got, err := n.nextOrdinal(context.Background())
		if err != nil {
			t.Fatalf("%s: nextOrdinal: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: nextOrdinal = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestTheNextOrdinalRefusesWhenTheTenantClientCannotBeBuilt(t *testing.T) {
	cause := errors.New("no credential")
	tenant := func(context.Context, string) (kubernetes.Interface, error) { return nil, cause }
	n := &tenantNodes{cluster: "liveweb", tenant: tenant, log: testLogger()}
	if _, err := n.nextOrdinal(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("nextOrdinal = %v, want the client's own failure wrapped: a read that failed is "+
			"not a cluster with no nodes, and naming node1 over an existing node1 is the collision "+
			"this list exists to avoid", err)
	}
}

func TestTheNodeTeardownStopsEveryNodeTheActuatorHolds(t *testing.T) {
	tenant, client := tenantClientFor(t, "liveweb-node1", "liveweb-node2")
	first := &fakeNodeHost{}
	second := &fakeNodeHost{}
	n := &tenantNodes{
		cluster: "liveweb", nodeName: "liveweb-node1", host: first, tenant: tenant,
		log: testLogger(), opts: nodeHostsOptions{StateDir: t.TempDir()},
		extra: map[int]heldNode{
			2: {name: "liveweb-node2", host: second},
		},
	}
	if err := n.Teardown(context.Background()); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if provisions, teardowns := first.calls(); provisions != 0 || teardowns != 1 {
		t.Fatalf("node1's host saw %d provisions, %d teardowns", provisions, teardowns)
	}
	if provisions, teardowns := second.calls(); provisions != 0 || teardowns != 1 {
		t.Fatalf("node2's host saw %d provisions, %d teardowns", provisions, teardowns)
	}
	if tenantNode(t, client, "liveweb-node1") || tenantNode(t, client, "liveweb-node2") {
		t.Fatal("the teardown left a node it held in the tenant cluster: the tenant's own node " +
			"list is the count the scale decision reads, and a node left there is one the cluster " +
			"can never be observed at zero without")
	}
	if len(n.extra) != 0 {
		t.Fatalf("the teardown left %d held nodes behind, so a later scale-up would reuse a name", len(n.extra))
	}
}
