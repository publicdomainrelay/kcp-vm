//go:build linux

package providermain

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
)

type syncedSecrets struct {
	*recordedSecrets

	mu    sync.Mutex
	live  []map[string]*v1alpha1.NodeClaimGrant
	fails error
}

func (s *syncedSecrets) factory(clusterDir string) nodemarket.NodeSecrets {
	s.recordedSecrets.factory(clusterDir)
	return s
}

func (s *syncedSecrets) Reconcile(_ context.Context, live map[string]*v1alpha1.NodeClaimGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*v1alpha1.NodeClaimGrant{}
	for name, grant := range live {
		out[name] = grant
	}
	s.live = append(s.live, out)
	return s.fails
}

func (s *syncedSecrets) reconciles() []map[string]*v1alpha1.NodeClaimGrant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]*v1alpha1.NodeClaimGrant{}, s.live...)
}

type vaultFixture struct {
	claims  *marketClaims
	clock   *lossClock
	secrets *syncedSecrets
	hosts   *nodeHosts
	key     string
	node    string
}

func newVaultFixture(t *testing.T) *vaultFixture {
	t.Helper()
	claims := &marketClaims{}
	tenant, _ := tenantClientFor(t)
	clock := &lossClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	secrets := &syncedSecrets{recordedSecrets: &recordedSecrets{}}
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
	return &vaultFixture{claims: claims, clock: clock, secrets: secrets, hosts: hosts, key: key,
		node: key + "-node1"}
}

func (f *vaultFixture) grant() v1alpha1.NodeClaimGrant {
	return v1alpha1.NodeClaimGrant{
		Issuer:   "https://bidder.example.com",
		Subject:  "vm:1",
		Audience: "socialweb.computer/node/" + f.key + "/" + f.node,
	}
}

func (f *vaultFixture) writeClaim(t *testing.T, phase v1alpha1.NodeClaimPhase, granted bool) v1alpha1.NodeClaimGrant {
	t.Helper()
	claim := v1alpha1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: f.node},
		Spec:       v1alpha1.NodeClaimSpec{Cluster: f.key},
		Status:     v1alpha1.NodeClaimStatus{Phase: phase},
	}
	grant := f.grant()
	if granted {
		claim.Status.Grant = &grant
	}
	if err := f.claims.WriteNodeClaim(context.Background(), testHomePrefix+"liveweb", claim); err != nil {
		t.Fatalf("writing the claim: %v", err)
	}
	return grant
}

func TestAdoptionReconcilesTheClusterOpenBaoWithTheLiveClaims(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	grant := f.writeClaim(t, v1alpha1.NodeClaimRunning, true)

	if err := f.hosts.adopt(ctx, f.key); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	reconciles := f.secrets.reconciles()
	if len(reconciles) != 1 {
		t.Fatalf("adopting the live claims of %s reconciled the cluster OpenBao %d times, want one: a "+
			"provider that restarts or crashes between a claim going away and its Revoke leaves a role, "+
			"a policy and a secret behind, and adoption is where a fresh process sees them",
			f.key, len(reconciles))
	}
	if len(reconciles[0]) != 1 {
		t.Fatalf("adoption reconciled with %v, want only the live node %s", reconciles[0], f.node)
	}
	got, ok := reconciles[0][f.node]
	if !ok || got == nil || *got != grant {
		t.Fatalf("adoption reconciled node %s with %v, want its live grant %+v", f.node, got, grant)
	}

	if err := f.hosts.reconcile(ctx, f.key); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(f.secrets.reconciles()); got != 1 {
		t.Fatalf("a second pass inside the %s rate limit reconciled the cluster OpenBao again (%d "+
			"reconciles): every pass reads and lists four paths, and the store is not a per-second thing",
			VaultSyncInterval, got)
	}

	f.clock.set(f.clock.Now().Add(VaultSyncInterval))
	if err := f.hosts.reconcile(ctx, f.key); err != nil {
		t.Fatalf("reconcile %s later: %v", VaultSyncInterval, err)
	}
	if got := len(f.secrets.reconciles()); got != 2 {
		t.Fatalf("a pass %s later made %d reconciles, want two", VaultSyncInterval, got)
	}
}

func TestReconcileNamesEveryLiveNodeAndOnlyThose(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	grant := f.writeClaim(t, v1alpha1.NodeClaimRunning, true)
	second := f.key + "-node2"
	if err := f.claims.WriteNodeClaim(ctx, testHomePrefix+"liveweb", v1alpha1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: second},
		Spec:       v1alpha1.NodeClaimSpec{Cluster: f.key},
		Status:     v1alpha1.NodeClaimStatus{Phase: v1alpha1.NodeClaimJoining},
	}); err != nil {
		t.Fatalf("writing the second claim: %v", err)
	}
	if err := f.claims.WriteNodeClaim(ctx, testHomePrefix+"liveweb", v1alpha1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: f.key + "-node3"},
		Spec:       v1alpha1.NodeClaimSpec{Cluster: f.key},
		Status:     v1alpha1.NodeClaimStatus{Phase: v1alpha1.NodeClaimReleased},
	}); err != nil {
		t.Fatalf("writing the released claim: %v", err)
	}
	if err := f.hosts.reconcile(ctx, f.key); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	reconciles := f.secrets.reconciles()
	if len(reconciles) != 1 {
		t.Fatalf("the pass reconciled %d times, want one", len(reconciles))
	}
	live := reconciles[0]
	if len(live) != 2 {
		t.Fatalf("the pass reconciled with %v, want the two live claims %s and %s: a claim the door "+
			"released names a machine that is gone, and its role, policy and secret are what nothing "+
			"else removes", live, f.node, second)
	}
	if got, ok := live[f.node]; !ok || got == nil || *got != grant {
		t.Fatalf("the grant of %s was reconciled as %v, want %+v", f.node, got, grant)
	}
	if got, ok := live[second]; !ok || got != nil {
		t.Fatalf("the claim of %s carries no grant and must be reconciled as a live node with none, "+
			"got %v", second, got)
	}
}

func TestReconcileKeepsANodeTheProviderStillHoldsWithNoClaim(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	nodes, err := f.hosts.forCluster(f.key)
	if err != nil {
		t.Fatalf("forCluster: %v", err)
	}
	if err := nodes.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := f.claims.DeleteNodeClaim(ctx, testHomePrefix+"liveweb", f.node); err != nil {
		t.Fatalf("deleting the claim: %v", err)
	}
	if err := f.hosts.reconcile(ctx, f.key); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	reconciles := f.secrets.reconciles()
	if len(reconciles) != 1 {
		t.Fatalf("the pass reconciled %d times, want one", len(reconciles))
	}
	if _, ok := reconciles[0][f.node]; !ok || len(reconciles[0]) != 1 {
		t.Fatalf("the pass reconciled with %v, want the node %s this process still holds: the door "+
			"deletes a claim while the guest it booted runs on until this provider gives it up, and a "+
			"reconcile that reads only the workspace takes the running guest's secret away",
			reconciles[0], f.node)
	}
}
