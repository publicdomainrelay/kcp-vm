//go:build linux

package providermain

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
)

type lossClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *lossClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lossClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type recordedSecrets struct {
	mu       sync.Mutex
	dirs     []string
	policies []string
	secrets  []string
	logins   []v1alpha1.NodeClaimGrant
	revokes  []string
}

func (r *recordedSecrets) factory(clusterDir string) nodemarket.NodeSecrets {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs = append(r.dirs, clusterDir)
	return r
}

func (r *recordedSecrets) EnsurePolicy(_ context.Context, node string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies = append(r.policies, node)
	return nil
}

func (r *recordedSecrets) WriteSecret(_ context.Context, node string, _ nodemarket.NodeSecret) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, node)
	return nil
}

func (r *recordedSecrets) EnsureLogin(_ context.Context, node string, grant v1alpha1.NodeClaimGrant) (nodemarket.NodeLogin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logins = append(r.logins, grant)
	return nodemarket.NodeLogin{Mount: "jwt-test", Role: node, Policy: "node-" + node}, nil
}

func (r *recordedSecrets) Revoke(_ context.Context, node string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revokes = append(r.revokes, node)
	return nil
}

type listeningLinks struct{ ticket string }

func (l listeningLinks) ReadClusterLink(_ context.Context, _, name string) (*v1alpha1.ClusterLink, error) {
	return &v1alpha1.ClusterLink{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     v1alpha1.ClusterLinkStatus{Phase: v1alpha1.ClusterLinkListening, Ticket: l.ticket},
	}, nil
}

type lossFixture struct {
	claims  *marketClaims
	clock   *lossClock
	secrets *recordedSecrets
	hosts   *nodeHosts
	key     string
	node    string
	tenant  tenantClient
	opts    nodeHostsOptions
}

func newLossFixture(t *testing.T) *lossFixture {
	t.Helper()
	claims := &marketClaims{}
	tenant, _ := tenantClientFor(t)
	clock := &lossClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	secrets := &recordedSecrets{}
	opts := marketOptions(t, claims, tenant)
	opts.Now = clock.Now
	opts.NodeSecrets = secrets.factory
	opts.Links = listeningLinks{ticket: "apiserver-ticket"}
	hosts, err := newNodeHosts(opts)
	if err != nil {
		t.Fatalf("newNodeHosts: %v", err)
	}
	key := clusterKey(t, "liveweb")
	clusterDir, err := clusterprovider.ClusterDataDir(opts.StateDir, key)
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
	nodes, err := hosts.forCluster(key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	node := key + "-node1"
	claims.setPhase(testHomePrefix+"liveweb", node, v1alpha1.NodeClaimRunning)
	return &lossFixture{claims: claims, clock: clock, secrets: secrets, hosts: hosts, key: key,
		node: node, tenant: tenant, opts: opts}
}

func (f *lossFixture) registerNode(t *testing.T, status corev1.ConditionStatus, since time.Time) {
	t.Helper()
	client, err := f.tenant(context.Background(), f.key)
	if err != nil {
		t.Fatalf("tenant client: %v", err)
	}
	_, err = client.CoreV1().Nodes().Create(context.Background(), &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: f.node},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: status, LastTransitionTime: metav1.NewTime(since),
		}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("registering the Node: %v", err)
	}
}

func (f *lossFixture) setReady(t *testing.T, status corev1.ConditionStatus, since time.Time) {
	t.Helper()
	client, err := f.tenant(context.Background(), f.key)
	if err != nil {
		t.Fatalf("tenant client: %v", err)
	}
	node, err := client.CoreV1().Nodes().Get(context.Background(), f.node, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the Node: %v", err)
	}
	node.Status.Conditions = []corev1.NodeCondition{{
		Type: corev1.NodeReady, Status: status, LastTransitionTime: metav1.NewTime(since),
	}}
	if _, err := client.CoreV1().Nodes().UpdateStatus(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating the Node's Ready condition: %v", err)
	}
}

func (f *lossFixture) nodePresent(t *testing.T) bool {
	t.Helper()
	client, err := f.tenant(context.Background(), f.key)
	if err != nil {
		t.Fatalf("tenant client: %v", err)
	}
	_, err = client.CoreV1().Nodes().Get(context.Background(), f.node, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatalf("reading the Node: %v", err)
	}
	return true
}

func TestAMarketNodeNotReadyFor89sIsKeptAndAt90sIsReleased(t *testing.T) {
	for _, status := range []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionUnknown} {
		t.Run(string(status), func(t *testing.T) {
			f := newLossFixture(t)
			ctx := context.Background()
			f.registerNode(t, corev1.ConditionTrue, f.clock.Now().Add(-time.Minute))
			if _, err := f.hosts.confirm(ctx, f.key); err != nil {
				t.Fatalf("confirm while Ready: %v", err)
			}
			wentNotReady := f.clock.Now()
			f.setReady(t, status, wentNotReady)

			f.clock.set(wentNotReady.Add(89 * time.Second))
			outcome, err := f.hosts.confirm(ctx, f.key)
			if err != nil {
				t.Fatalf("confirm at 89s: %v", err)
			}
			if f.claims.deletes != 0 || f.claims.held() != 1 || !f.nodePresent(t) {
				t.Fatalf("at 89s NotReady (%s) the claim deletes are %d, claims held %d, Node present %v; "+
					"want the node kept: the loss grace is 90s", status, f.claims.deletes,
					f.claims.held(), f.nodePresent(t))
			}
			if len(outcome.lost) != 0 {
				t.Fatalf("at 89s the pass reported %v lost", outcome.lost)
			}

			f.clock.set(wentNotReady.Add(90 * time.Second))
			outcome, err = f.hosts.confirm(ctx, f.key)
			if err != nil {
				t.Fatalf("confirm at 90s: %v", err)
			}
			if f.claims.deletes != 1 || f.claims.held() != 0 {
				t.Fatalf("at 90s NotReady (%s) the claim deletes are %d and %d claims are held; want "+
					"the claim deleted so the door releases the machine", status, f.claims.deletes,
					f.claims.held())
			}
			if f.nodePresent(t) {
				t.Fatal("at 90s the tenant Node is still there; its pods stay bound to a machine " +
					"that is gone")
			}
			if !slices.Equal(outcome.lost, []string{f.node}) {
				t.Fatalf("the pass reported %v lost, want [%s]", outcome.lost, f.node)
			}
			if !slices.Equal(f.secrets.revokes, []string{f.node}) {
				t.Fatalf("the release revoked %v in the cluster OpenBao, want [%s]", f.secrets.revokes, f.node)
			}
		})
	}
}

func TestANodeTheClusterNeverReportedIsNotTouchedByTheLossRule(t *testing.T) {
	f := newLossFixture(t)
	f.clock.set(f.clock.Now().Add(time.Hour))
	if _, err := f.hosts.confirm(context.Background(), f.key); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if f.claims.deletes != 0 || f.claims.held() != 1 {
		t.Fatalf("a claim whose node the cluster never reported was released by the loss rule "+
			"(deletes %d, held %d); the confirm and giving-up rules own that node", f.claims.deletes,
			f.claims.held())
	}
}

func TestAReadyNodeIsNotLostHoweverOldItsTransition(t *testing.T) {
	f := newLossFixture(t)
	f.registerNode(t, corev1.ConditionTrue, f.clock.Now().Add(-time.Hour))
	if _, err := f.hosts.confirm(context.Background(), f.key); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if f.claims.deletes != 0 || !f.nodePresent(t) {
		t.Fatalf("a Ready node was released (deletes %d)", f.claims.deletes)
	}
}

func TestTheConfirmPassWritesTheNodesSecretsAndTurnsAGrantIntoALogin(t *testing.T) {
	f := newLossFixture(t)
	wantDir, err := clusterprovider.ClusterDataDir(f.opts.StateDir, f.key)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	if len(f.secrets.dirs) == 0 || f.secrets.dirs[0] != wantDir {
		t.Fatalf("the node's OpenBao was opened over %v, want the cluster's own data dir %s",
			f.secrets.dirs, wantDir)
	}
	if !slices.Equal(f.secrets.policies, []string{f.node}) || !slices.Equal(f.secrets.secrets, []string{f.node}) {
		t.Fatalf("Provision wrote policies %v and secrets %v, want one of each for %s",
			f.secrets.policies, f.secrets.secrets, f.node)
	}
	grant := v1alpha1.NodeClaimGrant{
		Issuer: "https://bidder.example.com", Subject: "vm:1",
		Audience: "socialweb.computer/node/" + f.key + "/" + f.node,
	}
	f.claims.mu.Lock()
	claim := f.claims.claims[testHomePrefix+"liveweb/"+f.node]
	claim.Status.Grant = &grant
	f.claims.claims[testHomePrefix+"liveweb/"+f.node] = claim
	f.claims.mu.Unlock()
	for range 2 {
		if _, err := f.hosts.confirm(context.Background(), f.key); err != nil {
			t.Fatalf("confirm: %v", err)
		}
	}
	if !slices.Equal(f.secrets.logins, []v1alpha1.NodeClaimGrant{grant}) {
		t.Fatalf("two passes over a granted claim made logins %v, want exactly the grant once", f.secrets.logins)
	}
}

func TestANodeThatNeverWasReadyGetsTheJoinGraceNotTheLossGrace(t *testing.T) {
	f := newLossFixture(t)
	ctx := context.Background()
	registered := f.clock.Now()
	f.registerNode(t, corev1.ConditionFalse, registered)
	for _, at := range []time.Duration{NodeLossGrace, NodeJoinGrace - time.Second} {
		f.clock.set(registered.Add(at))
		if _, err := f.hosts.confirm(ctx, f.key); err != nil {
			t.Fatalf("confirm at %s: %v", at, err)
		}
		if f.claims.deletes != 0 || !f.nodePresent(t) {
			t.Fatalf("a node that registered and was never Ready was released %s after registering "+
				"(deletes %d). Measured on prod 2026-10-07T22:52:55Z: a market node registered at "+
				"22:51:24Z, its kubelet still starting, and the 90s loss rule released it before it "+
				"could become Ready; a node that has not been Ready yet is joining, not lost",
				at, f.claims.deletes)
		}
	}
	if NodeJoinGrace > 2*time.Minute {
		t.Fatalf("the default join grace is %s; a broken node held longer than 2m stalls every debugging "+
			"loop (user, 2026-10-07: 10 minutes kills the dev flow); a longer hold is an explicit %s",
			NodeJoinGrace, "SOCIALWEB_CLUSTERPROVIDER_NODE_JOIN_GRACE")
	}
	f.clock.set(registered.Add(NodeJoinGrace))
	outcome, err := f.hosts.confirm(ctx, f.key)
	if err != nil {
		t.Fatalf("confirm at the join grace: %v", err)
	}
	if f.claims.deletes != 1 || f.nodePresent(t) || !slices.Equal(outcome.lost, []string{f.node}) {
		t.Fatalf("at the join grace a never-Ready node: deletes %d, Node present %v, lost %v; want it "+
			"released and replaced", f.claims.deletes, f.nodePresent(t), outcome.lost)
	}
}

func TestTheJoinGraceIsConfigurable(t *testing.T) {
	cfg, err := load(envFor(map[string]string{"SOCIALWEB_CLUSTERPROVIDER_NODE_JOIN_GRACE": "15m"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.NodeJoinGrace != 15*time.Minute {
		t.Fatalf("NodeJoinGrace = %s, want 15m from SOCIALWEB_CLUSTERPROVIDER_NODE_JOIN_GRACE", cfg.NodeJoinGrace)
	}
}

func TestAClaimTheDoorMarkedLostIsReleasedAtOnceNotAfterTheLossGrace(t *testing.T) {
	f := newLossFixture(t)
	ctx := context.Background()
	f.registerNode(t, corev1.ConditionTrue, f.clock.Now().Add(-time.Minute))
	if _, err := f.hosts.confirm(ctx, f.key); err != nil {
		t.Fatalf("confirm while Ready: %v", err)
	}
	f.claims.setPhase(testHomePrefix+"liveweb", f.node, v1alpha1.NodeClaimLost)
	outcome, err := f.hosts.confirm(ctx, f.key)
	if err != nil {
		t.Fatalf("confirm after the door marked the claim Lost: %v", err)
	}
	if f.claims.deletes != 1 || f.claims.held() != 0 || f.nodePresent(t) {
		t.Fatalf("a claim the door marked Lost (the bidder reported its guest terminated) left deletes %d, "+
			"held %d, Node present %v; want it released in the same pass, with no 90s NotReady wait",
			f.claims.deletes, f.claims.held(), f.nodePresent(t))
	}
	if !slices.Equal(outcome.lost, []string{f.node}) || outcome.declinedReason() != "" {
		t.Fatalf("the pass reported lost %v and declined %q; want the node lost and no market back-off, so "+
			"the scale pass rents its replacement", outcome.lost, outcome.declinedReason())
	}
	if _, err := f.hosts.confirm(ctx, f.key); err != nil || f.claims.deletes != 1 {
		t.Fatalf("a second pass deleted again (deletes %d, err %v)", f.claims.deletes, err)
	}
}

type pathProbe struct {
	mu sync.Mutex
	up bool
}

func (p *pathProbe) set(up bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.up = up
}

func (p *pathProbe) PathUp(context.Context, string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.up
}

func newPathFixture(t *testing.T, startUp bool) (*lossFixture, *pathProbe) {
	t.Helper()
	probe := &pathProbe{up: startUp}
	f := newLossFixture(t)
	f.hosts.opts.PathUp = probe.PathUp
	n, err := f.hosts.forCluster(f.key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	n.(*tenantNodes).opts.PathUp = probe.PathUp
	return f, probe
}

func (f *lossFixture) confirmAt(t *testing.T, at time.Time) confirmOutcome {
	t.Helper()
	f.clock.set(at)
	outcome, err := f.hosts.confirm(context.Background(), f.key)
	if err != nil {
		t.Fatalf("confirm at %s: %v", at.Format(time.RFC3339), err)
	}
	return outcome
}

func TestANodeNotReadyDuringOurLinkOutageIsKeptFor90sAfterTheLinkReturns(t *testing.T) {
	f, probe := newPathFixture(t, true)
	start := f.clock.Now()
	f.registerNode(t, corev1.ConditionTrue, start.Add(-time.Minute))
	f.confirmAt(t, start)
	probe.set(false)
	f.confirmAt(t, start.Add(5*time.Second))
	wentNotReady := start.Add(45 * time.Second)
	f.setReady(t, corev1.ConditionUnknown, wentNotReady)
	for _, at := range []time.Duration{time.Minute, 3 * time.Minute, 5 * time.Minute} {
		f.confirmAt(t, start.Add(at))
		if f.claims.deletes != 0 {
			t.Fatalf("%s into our own link outage a node NotReady since %s was released; our outage "+
				"is never billed to a tenant's VM", at, wentNotReady.Format(time.RFC3339))
		}
	}
	linkBack := start.Add(5*time.Minute + 10*time.Second)
	probe.set(true)
	f.confirmAt(t, linkBack)
	f.confirmAt(t, linkBack.Add(89*time.Second))
	if f.claims.deletes != 0 {
		t.Fatalf("89s after our link returned the node was released (deletes %d); the NotReady clock "+
			"counts from when our side of the path is back, not from its lastTransitionTime", f.claims.deletes)
	}
	outcome := f.confirmAt(t, linkBack.Add(90*time.Second))
	if f.claims.deletes != 1 || !slices.Equal(outcome.lost, []string{f.node}) {
		t.Fatalf("90s after our link returned a node still NotReady was kept (deletes %d, lost %v)",
			f.claims.deletes, outcome.lost)
	}
}

func TestANodeNotReadyDuringAClusterColdStartIsKeptFor90sAfterTheApiserverAnswers(t *testing.T) {
	f, probe := newPathFixture(t, false)
	start := f.clock.Now()
	f.registerNode(t, corev1.ConditionUnknown, start.Add(-10*time.Minute))
	f.confirmAt(t, start)
	f.confirmAt(t, start.Add(10*time.Second))
	if f.claims.deletes != 0 {
		t.Fatalf("a node was released while its cluster's apiserver did not answer (deletes %d)", f.claims.deletes)
	}
	answered := start.Add(20 * time.Second)
	probe.set(true)
	f.confirmAt(t, answered)
	f.confirmAt(t, answered.Add(89*time.Second))
	if f.claims.deletes != 0 {
		t.Fatalf("89s after a cold-started cluster's apiserver answered, a node NotReady since before the "+
			"cold start was released (deletes %d)", f.claims.deletes)
	}
	f.confirmAt(t, answered.Add(90*time.Second))
	if f.claims.deletes != 1 {
		t.Fatalf("90s after the apiserver answered the node is still held (deletes %d)", f.claims.deletes)
	}
}

func TestANodeNotReadyBeforeAProviderRestartIsStillReleasedOnTime(t *testing.T) {
	f, _ := newPathFixture(t, true)
	start := f.clock.Now()
	f.registerNode(t, corev1.ConditionUnknown, start.Add(-100*time.Second))
	outcome := f.confirmAt(t, start)
	if f.claims.deletes != 1 || !slices.Equal(outcome.lost, []string{f.node}) {
		t.Fatalf("a provider whose first look found its path up rebased a node Unknown for 100s to its own "+
			"start (deletes %d, lost %v); the tenant's kcm kept running through our restart, so the "+
			"transition is real and the node is released on time", f.claims.deletes, outcome.lost)
	}
}

func TestTheJoinGraceIsRebasedToPathUpToo(t *testing.T) {
	f, probe := newPathFixture(t, true)
	start := f.clock.Now()
	f.confirmAt(t, start)
	probe.set(false)
	f.confirmAt(t, start.Add(10*time.Second))
	f.registerNode(t, corev1.ConditionFalse, start.Add(20*time.Second))
	f.confirmAt(t, start.Add(3*time.Minute))
	back := start.Add(4 * time.Minute)
	probe.set(true)
	f.confirmAt(t, back)
	f.confirmAt(t, back.Add(NodeJoinGrace-time.Second))
	if f.claims.deletes != 0 {
		t.Fatalf("a never-Ready node registered during our outage was released %s after the path came "+
			"back (deletes %d); the join grace counts from path up", NodeJoinGrace-time.Second, f.claims.deletes)
	}
	f.confirmAt(t, back.Add(NodeJoinGrace))
	if f.claims.deletes != 1 {
		t.Fatalf("the join grace from path up passed and the never-Ready node is still held (deletes %d)",
			f.claims.deletes)
	}
}

func TestAFlappingPathDoesNotPostponeReleaseForever(t *testing.T) {
	f, probe := newPathFixture(t, true)
	start := f.clock.Now()
	f.registerNode(t, corev1.ConditionTrue, start.Add(-time.Hour))
	f.confirmAt(t, start)
	dead := start.Add(5 * time.Second)
	f.setReady(t, corev1.ConditionUnknown, dead)
	at := start
	for i := 0; i < 60 && f.claims.deletes == 0; i++ {
		at = at.Add(5 * time.Second)
		probe.set(i%4 != 3)
		f.confirmAt(t, at)
	}
	if f.claims.deletes != 1 {
		t.Fatalf("a path that drops for one 5s pass in four never let a dead node go (deletes %d after "+
			"%s); a blip shorter than the outage threshold is not our outage", f.claims.deletes, at.Sub(dead))
	}
	if at.Sub(dead) > NodeLossGrace+10*time.Second {
		t.Fatalf("the flapping path postponed the release to %s after the node died, want about %s",
			at.Sub(dead), NodeLossGrace)
	}
}
