//go:build linux

package main

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
