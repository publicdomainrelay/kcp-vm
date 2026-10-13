package nodemarket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

type secretEvents struct {
	mu  sync.Mutex
	log []string
}

func (e *secretEvents) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, event)
}

func (e *secretEvents) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

type fakeSecrets struct {
	events *secretEvents

	mu       sync.Mutex
	policies map[string]bool
	secrets  map[string]NodeSecret
	logins   map[string]v1alpha1.NodeClaimGrant
	ensures  int
	revokes  []string

	policyErr error
}

func (f *fakeSecrets) EnsurePolicy(_ context.Context, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.policyErr != nil {
		return f.policyErr
	}
	if f.policies == nil {
		f.policies = map[string]bool{}
	}
	f.policies[node] = true
	f.events.add("policy " + node)
	return nil
}

func (f *fakeSecrets) WriteSecret(_ context.Context, node string, secret NodeSecret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secrets == nil {
		f.secrets = map[string]NodeSecret{}
	}
	f.secrets[node] = secret
	f.events.add("secret " + node)
	return nil
}

func (f *fakeSecrets) EnsureLogin(_ context.Context, node string, grant v1alpha1.NodeClaimGrant) (NodeLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logins == nil {
		f.logins = map[string]v1alpha1.NodeClaimGrant{}
	}
	f.logins[node] = grant
	f.ensures++
	f.events.add("login " + node)
	return NodeLogin{Mount: "jwt-test", Role: node, Policy: "node-" + node}, nil
}

func (f *fakeSecrets) Revoke(_ context.Context, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokes = append(f.revokes, node)
	delete(f.policies, node)
	delete(f.secrets, node)
	delete(f.logins, node)
	f.events.add("revoke " + node)
	return nil
}

func (f *fakeSecrets) secret(node string) (NodeSecret, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.secrets[node]
	return s, ok
}

type fakeLinks struct {
	mu    sync.Mutex
	links map[string]v1alpha1.ClusterLink
	reads []string
}

func (f *fakeLinks) ReadClusterLink(_ context.Context, workspace, name string) (*v1alpha1.ClusterLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, workspace+"/"+name)
	link, ok := f.links[workspace+"/"+name]
	if !ok {
		return nil, nil
	}
	return &link, nil
}

func (f *fakeLinks) listen(workspace, name, ticket string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.links == nil {
		f.links = map[string]v1alpha1.ClusterLink{}
	}
	link := f.links[workspace+"/"+name]
	link.Status.Phase = v1alpha1.ClusterLinkListening
	link.Status.Ticket = ticket
	f.links[workspace+"/"+name] = link
}

type orderedClaims struct {
	*fakeClaims
	events *secretEvents
}

func (o orderedClaims) WriteNodeClaim(ctx context.Context, workspace string, claim v1alpha1.NodeClaim) error {
	o.events.add("claim " + claim.Name)
	return o.fakeClaims.WriteNodeClaim(ctx, workspace, claim)
}

func secretOptions(t *testing.T) (Options, *fakeClaims, *fakeSecrets, *fakeLinks, *secretEvents) {
	t.Helper()
	events := &secretEvents{}
	claims := &fakeClaims{}
	opts := testOptions(t, orderedClaims{fakeClaims: claims, events: events})
	secrets := &fakeSecrets{events: events}
	links := &fakeLinks{}
	opts.Secrets = secrets
	opts.Links = links
	opts.ReleaseTimeout = 5 * time.Second
	opts.Poll = time.Millisecond
	return opts, claims, secrets, links, events
}

func TestProvisionWritesTheNodePolicyAndSecretBeforeTheClaim(t *testing.T) {
	opts, _, secrets, links, events := secretOptions(t)
	links.listen(testWorkspace, "liveweb-apiserver", "nodeticket-apiserver")
	links.listen(testWorkspace, "liveweb-openbao", "openbao-ticket")
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	want := []string{"policy " + testNodeName, "secret " + testNodeName, "claim " + testNodeName}
	if got := events.seen(); !slices.Equal(got, want) {
		t.Fatalf("Provision did %v, want %v: the claim is what makes the door rent a machine, and a "+
			"machine that boots before its policy and secret exist has nothing to log in to", got, want)
	}
	secret, ok := secrets.secret(testNodeName)
	if !ok {
		t.Fatal("no secret was written for the node")
	}
	password, err := os.ReadFile(NodePasswordPath(opts.DataDir))
	if err != nil {
		t.Fatalf("reading the minted node password: %v", err)
	}
	sum := sha256.Sum256([]byte("server-ca"))
	wantToken := "K10" + hex.EncodeToString(sum[:]) + "::server:join-token"
	for _, field := range []struct{ name, got, want string }{
		{"token", secret.Token, wantToken},
		{"nodePassword", secret.NodePassword, strings.TrimSpace(string(password))},
		{"serverCA", secret.ServerCA, "server-ca"},
		{"apiserverTicket", secret.ApiserverTicket, "nodeticket-apiserver"},
		{"advertiseAddress", secret.AdvertiseAddress, "100.127.255.254"},
	} {
		if field.got != field.want {
			t.Errorf("secret field %s is %q, want %q -- the guest hands these to k3s agent verbatim",
				field.name, field.got, field.want)
		}
	}
	if secret.ApiserverPort != 24000 {
		t.Errorf("secret field apiserverPort is %d, want 24000", secret.ApiserverPort)
	}
	if secret.NodePassword == "" {
		t.Error("the node password in the secret is empty")
	}
	if got := links.reads; len(got) == 0 || !slices.Contains(got, testWorkspace+"/liveweb-apiserver") || slices.ContainsFunc(got, func(r string) bool { return !strings.HasPrefix(r, testWorkspace+"/") }) {
		t.Errorf("the ticket was read from %v, want the ClusterLink liveweb-apiserver in the "+
			"cluster's home workspace %s", got, testWorkspace)
	}
}

func TestNothingIsRentedUntilBothClusterLinksListen(t *testing.T) {
	opts, claims, secrets, links, events := secretOptions(t)
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	for _, step := range []struct {
		name   string
		listen func()
	}{
		{"no link listens", func() {}},
		{"only the apiserver link listens", func() {
			links.listen(testWorkspace, "liveweb-apiserver", "apiserver-ticket")
		}},
	} {
		step.listen()
		done := make(chan error, 1)
		go func() { done <- host.Provision(ctx) }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrLinksNotListening) {
				t.Fatalf("%s: Provision returned %v, want ErrLinksNotListening -- a guest rented "+
					"now is handed no OpenBao ticket and no apiserver ticket, so it can only fail", step.name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Provision waited on a link; the pass holds every other cluster while it waits",
				step.name)
		}
		if got := claims.held(); got != 0 {
			t.Fatalf("%s: %d claims written, want 0", step.name, got)
		}
		if got := events.seen(); len(got) != 0 {
			t.Fatalf("%s: Provision wrote %v before both links listened, want nothing", step.name, got)
		}
	}
	links.listen(testWorkspace, "liveweb-openbao", "openbao-ticket")
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("Provision once both links listen: %v", err)
	}
	if got := claims.held(); got != 1 {
		t.Fatalf("%d claims after both links listened, want 1", got)
	}
	secret, ok := secrets.secret(testNodeName)
	if !ok || secret.ApiserverTicket != "apiserver-ticket" {
		t.Fatalf("secret %+v (written %v), want the apiserver link's ticket", secret, ok)
	}
}

func TestAGrantOnTheClaimBecomesOneJWTRole(t *testing.T) {
	opts, claims, secrets, links, _ := secretOptions(t)
	links.listen(testWorkspace, "liveweb-apiserver", "ticket")
	links.listen(testWorkspace, "liveweb-openbao", "openbao-ticket")
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := host.SyncSecrets(ctx); err != nil {
		t.Fatalf("SyncSecrets with no grant: %v", err)
	}
	if secrets.ensures != 0 {
		t.Fatalf("a role was written before the door wrote a grant")
	}
	grant := v1alpha1.NodeClaimGrant{
		Issuer:   "https://bidder.example.com",
		Subject:  "vm:abc",
		Audience: "socialweb.computer/node/liveweb/" + testNodeName,
	}
	claims.mu.Lock()
	claim := claims.claims[claims.key(testWorkspace, testNodeName)]
	claim.Status.Phase = v1alpha1.NodeClaimProvisioning
	claim.Status.Grant = &grant
	claims.claims[claims.key(testWorkspace, testNodeName)] = claim
	claims.mu.Unlock()
	for range 3 {
		if err := host.SyncSecrets(ctx); err != nil {
			t.Fatalf("SyncSecrets with a grant: %v", err)
		}
	}
	if secrets.ensures != 1 {
		t.Fatalf("three passes over one grant wrote the login %d times, want once", secrets.ensures)
	}
	if got := secrets.logins[testNodeName]; got != grant {
		t.Fatalf("the login was bound to %+v, want the door's grant %+v", got, grant)
	}
}

func TestAReleaseRemovesTheNodesVaultState(t *testing.T) {
	opts, claims, secrets, links, events := secretOptions(t)
	links.listen(testWorkspace, "liveweb-apiserver", "ticket")
	links.listen(testWorkspace, "liveweb-openbao", "openbao-ticket")
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.setFinalizer(testWorkspace, testNodeName)
	claims.release = func(string) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			claims.doorDestroysTheVM(testWorkspace, testNodeName)
		}()
	}
	if err := host.Teardown(ctx); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if !slices.Equal(secrets.revokes, []string{testNodeName}) {
		t.Fatalf("the release revoked %v, want exactly the released node", secrets.revokes)
	}
	if err := host.SyncSecrets(ctx); err != nil {
		t.Fatalf("SyncSecrets after release: %v", err)
	}
	if _, ok := secrets.secret(testNodeName); ok {
		t.Fatalf("a pass after the release wrote the secret back: %v", events.seen())
	}
}

func TestAPolicyThatCannotBeWrittenRentsNothing(t *testing.T) {
	opts, claims, secrets, _, _ := secretOptions(t)
	secrets.policyErr = errors.New("vault sealed")
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err == nil {
		t.Fatal("Provision succeeded while the node's policy could not be written")
	}
	if got := claims.held(); got != 0 {
		t.Fatalf("%d claims written while the node's policy could not be", got)
	}
}

func TestSecretsWithoutALinkReaderAreRefused(t *testing.T) {
	opts, _, _, _, _ := secretOptions(t)
	opts.Links = nil
	if _, err := New(opts); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("New with secrets and no link reader: %v, want ErrIncomplete", err)
	}
}
