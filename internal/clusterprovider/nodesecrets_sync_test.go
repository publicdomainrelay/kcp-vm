package clusterprovider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const staleMountA = "jwt-aaaaaaaaaaaa"

const staleMountB = "jwt-bbbbbbbbbbbb"

const staleMountC = "jwt-cccccccccccc"

const staleMountD = "jwt-dddddddddddd"

const notANodeMount = "jwt-zzzzzzzzzzzz"

type vaultSyncSeed struct {
	ctx       context.Context
	root      *openbao.Client
	vault     *NodeVault
	live      string
	grant     v1alpha1.NodeClaimGrant
	liveMount string
}

func seedVaultSync(t *testing.T, root *openbao.Client) vaultSyncSeed {
	t.Helper()
	issuer := startTestIssuer(t)
	ctx := context.Background()
	vault := NewNodeVaultWith(func() (*openbao.Client, error) { return root, nil })
	seed := vaultSyncSeed{ctx: ctx, root: root, vault: vault, live: "c0ffee-node1"}
	secret := nodemarketSecret(seed.live)
	if err := vault.EnsurePolicy(ctx, seed.live); err != nil {
		t.Fatalf("EnsurePolicy %s: %v", seed.live, err)
	}
	if err := vault.WriteSecret(ctx, seed.live, secret); err != nil {
		t.Fatalf("WriteSecret %s: %v", seed.live, err)
	}
	seed.grant = v1alpha1.NodeClaimGrant{
		Issuer:   issuer.url,
		Subject:  "vm:" + seed.live,
		Audience: "socialweb.computer/node/c0ffee/" + seed.live,
	}
	if _, err := vault.EnsureLogin(ctx, seed.live, seed.grant); err != nil {
		t.Fatalf("EnsureLogin %s: %v", seed.live, err)
	}
	seed.liveMount = vaultlayout.NodeJWTMount(issuer.url)
	for _, mount := range []string{staleMountA, staleMountB, staleMountC, staleMountD} {
		if err := root.EnableAuth(ctx, mount, "jwt"); err != nil {
			t.Fatalf("enabling the stale mount %s: %v", mount, err)
		}
	}
	writeStaleNodeRole(t, root, staleMountA, "dead-node")
	writeStaleNodeRole(t, root, staleMountB, seed.live)
	if err := root.PutPolicy(ctx, vaultlayout.NodeJWTPolicyName("dead-node"),
		vaultlayout.NodeJWTPolicyHCL("dead-node")); err != nil {
		t.Fatalf("writing the stale policy: %v", err)
	}
	if err := vault.WriteSecret(ctx, "dead-node", nodemarketSecret("dead-node")); err != nil {
		t.Fatalf("writing the stale KV entry: %v", err)
	}
	if err := root.PutPolicy(ctx, vaultlayout.PolicyPostgres,
		"path \"secret/data/k3s/postgres\" {\n  capabilities = [\"read\"]\n}\n"); err != nil {
		t.Fatalf("writing the neighbouring policy: %v", err)
	}
	if err := root.EnableAppRole(ctx); err != nil {
		t.Fatalf("enabling the neighbouring approle mount: %v", err)
	}
	if err := root.EnableAuth(ctx, notANodeMount, "jwt"); err != nil {
		t.Fatalf("enabling the neighbouring jwt mount %s: %v", notANodeMount, err)
	}
	return seed
}

func nodemarketSecret(node string) nodemarket.NodeSecret {
	return nodemarket.NodeSecret{
		Token:            "K10abc::server:pw-" + node,
		NodePassword:     "node-password-" + node,
		ServerCA:         "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n",
		ApiserverTicket:  "ticket-" + node,
		AdvertiseAddress: "100.127.255.254",
		ApiserverPort:    24001,
	}
}

func writeStaleNodeRole(t *testing.T, client *openbao.Client, mount, node string) {
	t.Helper()
	if err := client.Write(context.Background(), "auth/"+mount+"/role/"+node, map[string]any{
		"role_type":       vaultlayout.NodeJWTRoleType,
		"bound_subject":   "vm:" + node,
		"bound_audiences": []string{"socialweb.computer/node/stale/" + node},
		"user_claim":      vaultlayout.NodeJWTUserClaim,
		"token_ttl":       vaultlayout.NodeJWTTokenTTL,
		"token_policies":  []string{vaultlayout.NodeJWTPolicyName(node)},
	}); err != nil {
		t.Fatalf("writing the stale role %s under auth/%s: %v", node, mount, err)
	}
}

func (s vaultSyncSeed) liveGrants() map[string]*v1alpha1.NodeClaimGrant {
	grant := s.grant
	return map[string]*v1alpha1.NodeClaimGrant{s.live: &grant}
}

func TestReconcileLeavesOnlyTheLiveNodesMaterialInTheClusterVault(t *testing.T) {
	root, _ := startDevBao(t)
	seed := seedVaultSync(t, root)
	ctx := seed.ctx
	live := seed.liveGrants()

	if err := seed.vault.Reconcile(ctx, live); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	auths, err := root.AuthMounts(ctx)
	if err != nil {
		t.Fatalf("AuthMounts: %v", err)
	}
	for _, stale := range []string{staleMountA, staleMountB, staleMountC, staleMountD} {
		if _, ok := auths[stale]; ok {
			t.Fatalf("the stale jwt mount %s survived the reconcile: %v -- a mount left with no role "+
				"can log nobody in and is the residue of an issuer that no live node uses", stale, auths)
		}
	}
	if auths[seed.liveMount].Type != "jwt" {
		t.Fatalf("the live node's mount %s was removed; roles and KV are per node and the node is live",
			seed.liveMount)
	}
	if auths[notANodeMount].Type != "jwt" {
		t.Fatalf("the mount %s does not carry the node naming and must never be touched: %v",
			notANodeMount, auths)
	}
	if auths["approle"].Type != "approle" {
		t.Fatalf("the approle mount was removed by the node reconcile: %v", auths)
	}

	roles, err := root.List(ctx, "auth/"+seed.liveMount+"/role")
	if err != nil {
		t.Fatalf("listing the live mount's roles: %v", err)
	}
	if !slices.Equal(roles, []string{seed.live}) {
		t.Fatalf("the live mount's roles are %v, want only %s: a role is a login and a login with no "+
			"live claim behind it is one nothing revokes", roles, seed.live)
	}

	policies, err := root.List(ctx, "sys/policies/acl")
	if err != nil {
		t.Fatalf("listing policies: %v", err)
	}
	if !slices.Contains(policies, vaultlayout.NodeJWTPolicyName(seed.live)) {
		t.Fatalf("the live node's policy is gone: %v", policies)
	}
	if slices.Contains(policies, vaultlayout.NodeJWTPolicyName("dead-node")) {
		t.Fatalf("the stale policy node-dead-node survived: %v", policies)
	}
	for _, keep := range []string{vaultlayout.PolicyPostgres, "default", "root"} {
		if !slices.Contains(policies, keep) {
			t.Fatalf("the reconcile removed the policy %q, which does not carry the node naming: %v",
				keep, policies)
		}
	}

	entries, err := root.List(ctx, vaultlayout.NodesKVMount+"/metadata")
	if err != nil {
		t.Fatalf("listing the node KV entries: %v", err)
	}
	if !slices.Contains(entries, seed.live) {
		t.Fatalf("the live node's secret is gone: %v", entries)
	}
	if slices.Contains(entries, "dead-node") {
		t.Fatalf("the stale KV entry dead-node survived: %v", entries)
	}

	mounts, err := root.SecretMounts(ctx)
	if err != nil {
		t.Fatalf("SecretMounts: %v", err)
	}
	if _, ok := mounts[vaultlayout.NodesKVMount]; !ok {
		t.Fatalf("the node KV mount was removed: %v", mounts)
	}

	before := describeVault(t, root, seed)
	if err := seed.vault.Reconcile(ctx, live); err != nil {
		t.Fatalf("a second Reconcile: %v", err)
	}
	if after := describeVault(t, root, seed); after != before {
		t.Fatalf("a second Reconcile changed an already-synced vault:\n%s\nwant\n%s", after, before)
	}
}

func TestReconcileKeepsTheMountALiveNodesGrantNamesBeforeItsRoleIsWritten(t *testing.T) {
	root, _ := startDevBao(t)
	seed := seedVaultSync(t, root)
	ctx := seed.ctx
	issuer := startTestIssuer(t)
	mount := vaultlayout.NodeJWTMount(issuer.url)
	if err := root.EnableAuth(ctx, mount, "jwt"); err != nil {
		t.Fatalf("enabling the live node's issuer mount: %v", err)
	}
	grant := v1alpha1.NodeClaimGrant{
		Issuer:   issuer.url,
		Subject:  "vm:late-node",
		Audience: "socialweb.computer/node/c0ffee/late-node",
	}
	live := seed.liveGrants()
	live["late-node"] = &grant

	if err := seed.vault.Reconcile(ctx, live); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	auths, err := root.AuthMounts(ctx)
	if err != nil {
		t.Fatalf("AuthMounts: %v", err)
	}
	if auths[mount].Type != "jwt" {
		t.Fatalf("the mount %s of the live node late-node's grant issuer was disabled: a mount is "+
			"enabled before its role is written and a write that failed is retried, so a mount a live "+
			"claim names is not residue whatever it holds today", mount)
	}
}

func TestReconcileWithNoLiveNodeRemovesEveryNode(t *testing.T) {
	root, _ := startDevBao(t)
	seed := seedVaultSync(t, root)
	ctx := seed.ctx

	if err := seed.vault.Reconcile(ctx, nil); err != nil {
		t.Fatalf("Reconcile with no live node: %v", err)
	}
	auths, err := root.AuthMounts(ctx)
	if err != nil {
		t.Fatalf("AuthMounts: %v", err)
	}
	if _, ok := auths[seed.liveMount]; ok {
		t.Fatalf("the mount of a node no claim names survived a reconcile with no live node: %v", auths)
	}
	policies, err := root.List(ctx, "sys/policies/acl")
	if err != nil {
		t.Fatalf("listing policies: %v", err)
	}
	if slices.Contains(policies, vaultlayout.NodeJWTPolicyName(seed.live)) {
		t.Fatalf("a released node's policy survived: %v", policies)
	}
	entries, err := root.List(ctx, vaultlayout.NodesKVMount+"/metadata")
	if err != nil {
		t.Fatalf("listing the node KV entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a released node's secret survived: %v", entries)
	}
}

func TestReconcileDeletesNothingWhenAListFails(t *testing.T) {
	root, addr := startDevBao(t)
	seed := seedVaultSync(t, root)
	ctx := seed.ctx

	client, broken := failingListClient(t, addr)
	vault := NewNodeVaultWith(func() (*openbao.Client, error) { return client, nil })
	broken.Store(true)
	err := vault.Reconcile(ctx, nil)
	if err == nil {
		t.Fatal("Reconcile reported no error with every LIST failing; an unreadable list is not an " +
			"empty list, and a reconcile that reads a failure as nothing-declared deletes the live " +
			"nodes' roles the first time the vault is slow")
	}
	for _, want := range []string{"/role", "sys/policies/acl", vaultlayout.NodesKVMount} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the reconcile's failed reads are reported as %q, which names no failure of %s: "+
				"each of the three reads is its own deletion decision, and one swallowed is one that "+
				"deletes everything the next unreadable read did not declare", err, want)
		}
	}
	broken.Store(false)

	auths, err := root.AuthMounts(ctx)
	if err != nil {
		t.Fatalf("AuthMounts: %v", err)
	}
	for _, mount := range []string{staleMountA, staleMountB, staleMountC, staleMountD, seed.liveMount} {
		if _, ok := auths[mount]; !ok {
			t.Fatalf("the mount %s was removed by a reconcile whose reads failed: %v", mount, auths)
		}
	}
	roles, err := root.List(ctx, "auth/"+staleMountA+"/role")
	if err != nil {
		t.Fatalf("listing the stale mount's roles: %v", err)
	}
	if !slices.Contains(roles, "dead-node") {
		t.Fatalf("a role was removed by a reconcile whose reads failed: %v", roles)
	}
	policies, err := root.List(ctx, "sys/policies/acl")
	if err != nil {
		t.Fatalf("listing policies: %v", err)
	}
	for _, want := range []string{vaultlayout.NodeJWTPolicyName("dead-node"), vaultlayout.NodeJWTPolicyName(seed.live)} {
		if !slices.Contains(policies, want) {
			t.Fatalf("the policy %s was removed by a reconcile whose reads failed: %v", want, policies)
		}
	}
	entries, err := root.List(ctx, vaultlayout.NodesKVMount+"/metadata")
	if err != nil {
		t.Fatalf("listing the node KV entries: %v", err)
	}
	for _, want := range []string{"dead-node", seed.live} {
		if !slices.Contains(entries, want) {
			t.Fatalf("the KV entry %s was removed by a reconcile whose reads failed: %v", want, entries)
		}
	}

	if err := vault.Reconcile(ctx, nil); err != nil {
		t.Fatalf("Reconcile once the reads work again: %v", err)
	}
	entries, err = root.List(ctx, vaultlayout.NodesKVMount+"/metadata")
	if err != nil {
		t.Fatalf("listing the node KV entries after the clean reconcile: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a readable reconcile left %v behind", entries)
	}
}

func failingListClient(t *testing.T, addr string) (*openbao.Client, *atomic.Bool) {
	t.Helper()
	target, err := url.Parse(addr)
	if err != nil {
		t.Fatalf("parsing the dev vault's address %s: %v", addr, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	broken := &atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "LIST" && broken.Load() {
			http.Error(w, "the vault is unreadable", http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return openbao.New(server.URL, devRootToken), broken
}

func describeVault(t *testing.T, root *openbao.Client, seed vaultSyncSeed) string {
	t.Helper()
	ctx := seed.ctx
	auths, err := root.AuthMounts(ctx)
	if err != nil {
		t.Fatalf("AuthMounts: %v", err)
	}
	names := make([]string, 0, len(auths))
	for name := range auths {
		names = append(names, name)
	}
	slices.Sort(names)
	policies, err := root.List(ctx, "sys/policies/acl")
	if err != nil {
		t.Fatalf("listing policies: %v", err)
	}
	slices.Sort(policies)
	entries, err := root.List(ctx, vaultlayout.NodesKVMount+"/metadata")
	if err != nil {
		t.Fatalf("listing the node KV entries: %v", err)
	}
	slices.Sort(entries)
	return fmt.Sprintf("mounts %s policies %s kv %s", strings.Join(names, ","),
		strings.Join(policies, ","), strings.Join(entries, ","))
}
