//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/cloudprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scaledriver"
)

const adoptWorkspace = testHomePrefix + "liveweb"

type restartFixture struct {
	claims   *marketClaims
	clock    *lossClock
	client   *fake.Clientset
	opts     nodeHostsOptions
	key      string
	start    time.Time
	first    *nodeHosts
	before   *recordedSecrets
	after    *recordedSecrets
	hosts    *nodeHosts
	writes   int
	observer clusterprovider.NodeObserver

	mu       sync.Mutex
	cordoned []string
}

func newRestartFixture(t *testing.T, phases ...v1alpha1.NodeClaimPhase) *restartFixture {
	t.Helper()
	ctx := context.Background()
	f := &restartFixture{claims: &marketClaims{}, before: &recordedSecrets{}, after: &recordedSecrets{},
		clock: &lossClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}}
	f.start = f.clock.Now()
	tenant, client := tenantClientFor(t)
	f.client = client
	f.observer = clusterprovider.NodeObserver{Client: tenant, Operators: scale.OurOperators}
	f.opts = marketOptions(t, f.claims, tenant)
	f.opts.Now = f.clock.Now
	f.opts.Links = listeningLinks{ticket: "apiserver-ticket"}
	f.opts.Observe = f.observer.Observe
	f.key = clusterKey(t, "liveweb")
	clusterDir, err := clusterprovider.ClusterDataDir(f.opts.StateDir, f.key)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	for path, content := range map[string]string{
		clusterprovider.TokenPath(clusterDir):                           "server-token",
		filepath.Join(clusterstack.TLSDir(clusterDir), "server-ca.crt"): "server-ca",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	firstOpts := f.opts
	firstOpts.NodeSecrets = f.before.factory
	f.first, err = newNodeHosts(firstOpts)
	if err != nil {
		t.Fatalf("newNodeHosts (first process): %v", err)
	}
	nodes, err := f.first.forCluster(f.key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	for i, phase := range phases {
		if err := nodes.Provision(ctx); err != nil {
			t.Fatalf("Provision %d: %v", i+1, err)
		}
		f.claims.setPhase(adoptWorkspace, f.node(i+1), phase)
	}
	f.writes = f.claims.writes
	restartedOpts := f.opts
	restartedOpts.NodeSecrets = f.after.factory
	f.hosts, err = newNodeHosts(restartedOpts)
	if err != nil {
		t.Fatalf("newNodeHosts (restarted process): %v", err)
	}
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		return true, nil, nil
	})
	client.PrependReactor("update", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		n := action.(k8stesting.UpdateAction).GetObject().(*corev1.Node)
		if n.Spec.Unschedulable {
			f.mu.Lock()
			f.cordoned = append(f.cordoned, n.Name)
			f.mu.Unlock()
		}
		return false, nil, nil
	})
	return f
}

func (f *restartFixture) node(i int) string {
	return f.key + "-node" + string(rune('0'+i))
}

func (f *restartFixture) register(t *testing.T, i int, ready corev1.ConditionStatus, since time.Time) {
	t.Helper()
	_, err := f.client.CoreV1().Nodes().Create(context.Background(), &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: f.node(i),
			CreationTimestamp: metav1.NewTime(f.start.Add(time.Duration(i) * time.Second))},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: ready, LastTransitionTime: metav1.NewTime(since)}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("registering %s: %v", f.node(i), err)
	}
}

func (f *restartFixture) claim(i int) *v1alpha1.NodeClaim {
	claim, _ := f.claims.ReadNodeClaim(context.Background(), adoptWorkspace, f.node(i))
	return claim
}

func (f *restartFixture) present(t *testing.T, i int) bool {
	t.Helper()
	return tenantNode(t, f.client, f.node(i))
}

func (f *restartFixture) held() []string {
	names := []string{}
	for name := range f.hosts.heldNames(f.key) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *restartFixture) counted(t *testing.T) int {
	t.Helper()
	_, nodes, err := f.hosts.observeHeld(f.observer.Observe)(context.Background(), f.key)
	if err != nil {
		t.Fatalf("observing: %v", err)
	}
	return nodes
}

func (f *restartFixture) driver(t *testing.T) *scaledriver.Driver {
	t.Helper()
	d, err := scaledriver.New(f.hosts.forCluster, scaledriver.Options{
		Now:     f.clock.Now,
		Observe: f.hosts.observeHeld(f.observer.Observe),
		Log:     testLogger(),
	})
	if err != nil {
		t.Fatalf("scaledriver.New: %v", err)
	}
	return d
}

func (f *restartFixture) adopt(t *testing.T) {
	t.Helper()
	if err := f.hosts.adopt(context.Background(), f.key); err != nil {
		t.Fatalf("adopt: %v", err)
	}
}

func TestARestartedProviderAdoptsItsJoiningAndRunningNodesOnceEach(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimJoining, v1alpha1.NodeClaimRunning)
	f.register(t, 1, corev1.ConditionTrue, f.start)
	f.register(t, 2, corev1.ConditionTrue, f.start)
	ctx := context.Background()
	f.adopt(t)

	if want := []string{f.node(1), f.node(2)}; !slices.Equal(f.held(), want) {
		t.Fatalf("the restarted provider holds %v, want %v: a live claim the previous process wrote "+
			"is a node it rents, and one nothing holds is one no rule can confirm, lose or release",
			f.held(), want)
	}
	for range 2 {
		if err := f.hosts.reconcile(ctx, f.key); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if _, err := f.hosts.confirm(ctx, f.key); err != nil {
			t.Fatalf("confirm: %v", err)
		}
		if err := f.hosts.adopt(ctx, f.key); err != nil {
			t.Fatalf("adopt again: %v", err)
		}
	}
	if got := f.counted(t); got != 2 {
		t.Fatalf("two adopted nodes the cluster reports are counted as %d, want 2 (once each)", got)
	}
	if len(f.held()) != 2 {
		t.Fatalf("passes after the adoption hold %v; adopting again must not hold a node twice", f.held())
	}
	if f.claims.writes != f.writes || f.claims.deletes != 0 {
		t.Fatalf("adopting wrote %d claims and deleted %d; want none of either", f.claims.writes-f.writes,
			f.claims.deletes)
	}
	if len(f.after.policies) != 0 || len(f.after.secrets) != 0 || len(f.after.revokes) != 0 {
		t.Fatalf("the restarted provider wrote policies %v, secrets %v and revoked %v; the guests already "+
			"fetched their secrets, so nothing in OpenBao is written again", f.after.policies,
			f.after.secrets, f.after.revokes)
	}
	if f.claim(1).Status.Phase != v1alpha1.NodeClaimJoining || f.claim(2).Status.Phase != v1alpha1.NodeClaimRunning {
		t.Fatalf("the adopted claims read %q and %q afterwards", f.claim(1).Status.Phase, f.claim(2).Status.Phase)
	}
	dir, err := clusterprovider.ClusterDataDir(f.opts.StateDir, f.key)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	raw, err := os.ReadFile(clusterstack.NodeSnapshotPath(dir))
	if err != nil {
		t.Fatalf("reading the instance snapshot: %v", err)
	}
	var instances []cloudprovider.Instance
	if err := json.Unmarshal(raw, &instances); err != nil {
		t.Fatalf("decoding the instance snapshot: %v", err)
	}
	names := []string{}
	for _, in := range instances {
		names = append(names, in.NodeName)
	}
	if want := []string{f.node(1), f.node(2)}; !slices.Equal(names, want) {
		t.Fatalf("the instance snapshot names %v, want %v: the cloud controller manager deletes every "+
			"Node the snapshot does not name", names, want)
	}
}

func TestAnAdoptedNodeNotReadyFor90sIsReleased(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimRunning)
	f.register(t, 1, corev1.ConditionUnknown, f.start)
	f.adopt(t)
	f.clock.set(f.start.Add(NodeLossGrace))
	outcome, err := f.hosts.confirm(context.Background(), f.key)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !slices.Equal(outcome.lost, []string{f.node(1)}) || f.claim(1) != nil || f.present(t, 1) {
		t.Fatalf("an adopted node NotReady for 90s: lost %v, claim left %v, Node present %v; want it "+
			"released like a node this process rented", outcome.lost, f.claim(1) != nil, f.present(t, 1))
	}
	if !slices.Equal(f.after.revokes, []string{f.node(1)}) {
		t.Fatalf("the release revoked %v, want [%s]", f.after.revokes, f.node(1))
	}
}

func TestAnAdoptedIdleNodeIsScaledIn(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimRunning, v1alpha1.NodeClaimRunning, v1alpha1.NodeClaimRunning)
	for i := 1; i <= 3; i++ {
		f.register(t, i, corev1.ConditionTrue, f.start)
	}
	if _, err := f.client.CoreV1().Pods("default").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "work"},
		Spec:       corev1.PodSpec{NodeName: f.node(1)}, Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating the work pod: %v", err)
	}
	f.adopt(t)
	d := f.driver(t)
	if _, err := d.RunPass(context.Background(), f.key); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	f.clock.set(f.start.Add(scale.DefaultHold))
	dec, err := d.RunPass(context.Background(), f.key)
	if err != nil {
		t.Fatalf("pass past the hold: %v", err)
	}
	if dec.Action != scale.Release || dec.Release != f.node(3) {
		t.Fatalf("past the hold over three adopted nodes, one busy, decided %s %q, want Release %s: %s",
			dec.Action, dec.Release, f.node(3), dec.Reason)
	}
	if !slices.Equal(f.cordoned, []string{f.node(3)}) || f.claim(3) != nil || f.present(t, 3) ||
		!slices.Equal(f.after.revokes, []string{f.node(3)}) {
		t.Fatalf("%s: cordoned %v, claim left %v, Node present %v, revokes %v; want it cordoned, its "+
			"claim and Node deleted and its vault state revoked", f.node(3), f.cordoned, f.claim(3) != nil,
			f.present(t, 3), f.after.revokes)
	}
	if f.claim(1) == nil || !f.present(t, 1) {
		t.Fatalf("the busy adopted node %s was released", f.node(1))
	}
}

func TestTeardownToZeroReleasesAdoptedNodes(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimRunning, v1alpha1.NodeClaimJoining)
	f.register(t, 1, corev1.ConditionTrue, f.start)
	f.adopt(t)
	d := f.driver(t)
	var dec scale.Decision
	for _, after := range []time.Duration{0, scale.DefaultHold} {
		f.clock.set(f.start.Add(after))
		var err error
		dec, err = d.RunPass(context.Background(), f.key)
		if err != nil {
			t.Fatalf("pass at %s: %v", after, err)
		}
	}
	if dec.Action != scale.Teardown {
		t.Fatalf("a cluster with no intent past the hold decided %s: %s", dec.Action, dec.Reason)
	}
	if f.claim(1) != nil || f.claim(2) != nil || f.claims.deletes != 2 || f.present(t, 1) {
		t.Fatalf("teardown to zero left claims %v/%v (deletes %d) and Node %v; want both adopted "+
			"claims released, the joining one included", f.claim(1) != nil, f.claim(2) != nil,
			f.claims.deletes, f.present(t, 1))
	}
	if got := f.counted(t); got != 0 {
		t.Fatalf("after teardown to zero %d nodes are counted", got)
	}
}

func TestAProvisionAfterARestartSkipsAdoptedNames(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimRunning, v1alpha1.NodeClaimJoining)
	f.register(t, 1, corev1.ConditionTrue, f.start)
	f.adopt(t)
	nodes, err := f.hosts.forCluster(f.key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if f.claims.writes != f.writes+1 || f.claim(3) == nil {
		t.Fatalf("a provision after the restart wrote %d claims, node3 claimed %v; want exactly one, "+
			"named past the adopted %s and %s", f.claims.writes-f.writes, f.claim(3) != nil, f.node(1), f.node(2))
	}
	if f.claim(1).Status.Phase != v1alpha1.NodeClaimRunning || f.claim(2).Status.Phase != v1alpha1.NodeClaimJoining {
		t.Fatalf("the adopted claims were rewritten: %q and %q", f.claim(1).Status.Phase, f.claim(2).Status.Phase)
	}
	if got := f.counted(t); got != 3 {
		t.Fatalf("one joined, one joining and one just claimed count as %d, want 3", got)
	}
}

func TestAnEndedClaimIsNotAdopted(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimFailed, v1alpha1.NodeClaimReleased, v1alpha1.NodeClaimRunning)
	now := metav1.NewTime(f.start)
	f.claims.mu.Lock()
	deleting := f.claims.claims[adoptWorkspace+"/"+f.node(3)]
	deleting.DeletionTimestamp = &now
	f.claims.claims[adoptWorkspace+"/"+f.node(3)] = deleting
	f.claims.claims[adoptWorkspace+"/otherweb-node1"] = v1alpha1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "otherweb-node1"},
		Spec:       v1alpha1.NodeClaimSpec{Cluster: "otherweb", NodeName: "otherweb-node1"},
		Status:     v1alpha1.NodeClaimStatus{Phase: v1alpha1.NodeClaimRunning},
	}
	f.claims.mu.Unlock()
	f.adopt(t)
	if err := f.hosts.reconcile(context.Background(), f.key); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(f.held()) != 0 || f.counted(t) != 0 {
		t.Fatalf("Failed, Released, deleting and another cluster's claims left %v held and %d counted; "+
			"want none: an ended claim is a machine that is gone or going", f.held(), f.counted(t))
	}
	if f.claims.writes != f.writes || f.claims.deletes != 0 {
		t.Fatalf("not adopting wrote %d claims and deleted %d", f.claims.writes-f.writes, f.claims.deletes)
	}
}

func TestAProviderShutdownLeavesItsMarketNodesForTheNextOne(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimRunning, v1alpha1.NodeClaimJoining)
	p := &Provider{
		cfg:   Config{Join: "liveweb"},
		host:  materialiser.NewLocalHost(procgroup.Options{}),
		nodes: f.first,
		log:   testLogger(),
	}
	if err := p.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if f.claims.deletes != 0 || f.claim(1) == nil || f.claim(2) == nil || len(f.before.revokes) != 0 {
		t.Fatalf("a provider shutdown deleted %d claims and revoked %v; want none: the guests run on "+
			"without this process, and deleting the claim is what makes the door destroy the machine",
			f.claims.deletes, f.before.revokes)
	}
}

func TestTheFirstScalePassAfterARestartAdoptsBeforeItCounts(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimRunning, v1alpha1.NodeClaimJoining)
	f.register(t, 1, corev1.ConditionTrue, f.start)
	p := &Provider{nodes: f.hosts, nodeScale: f.driver(t), log: testLogger()}
	p.scale(context.Background(), f.key, "liveweb", true)
	if want := []string{f.node(1), f.node(2)}; !slices.Equal(f.held(), want) {
		t.Fatalf("after the first scale pass of a restarted provider it holds %v, want %v", f.held(), want)
	}
	if f.claims.writes != f.writes || f.claims.deletes != 0 {
		t.Fatalf("the first pass wrote %d claims and deleted %d", f.claims.writes-f.writes, f.claims.deletes)
	}
}

func TestAClusterSwitchedOffAfterARestartReleasesItsAdoptedNodes(t *testing.T) {
	f := newRestartFixture(t, v1alpha1.NodeClaimRunning, v1alpha1.NodeClaimJoining)
	f.register(t, 1, corev1.ConditionTrue, f.start)
	p := &Provider{host: materialiser.NewLocalHost(procgroup.Options{}), nodes: f.hosts, log: testLogger()}
	if err := p.reapOne(context.Background(), f.key); err != nil {
		t.Fatalf("reapOne: %v", err)
	}
	if f.claim(1) != nil || f.claim(2) != nil || f.claims.deletes != 2 {
		t.Fatalf("a cluster switched off after a restart left claims %v/%v (deletes %d); want both "+
			"released: a claim nothing holds is a machine nothing releases", f.claim(1) != nil,
			f.claim(2) != nil, f.claims.deletes)
	}
	if err := p.reapOne(context.Background(), f.key); err != nil {
		t.Fatalf("reapOne again: %v", err)
	}
	if f.claims.deletes != 2 {
		t.Fatalf("a second reap of the off cluster deleted %d claims in all", f.claims.deletes)
	}
}
