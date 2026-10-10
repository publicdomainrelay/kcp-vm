package nodemarket

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

type fakeClaims struct {
	mu sync.Mutex

	claims map[string]v1alpha1.NodeClaim

	reads   int
	writes  int
	deletes int

	release func(key string)

	readErr   error
	writeErr  error
	deleteErr error
}

func (f *fakeClaims) key(workspace, name string) string { return workspace + "/" + name }

func (f *fakeClaims) WriteNodeClaim(_ context.Context, workspace string, claim v1alpha1.NodeClaim) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.claims == nil {
		f.claims = map[string]v1alpha1.NodeClaim{}
	}
	f.claims[f.key(workspace, claim.Name)] = claim
	return nil
}

func (f *fakeClaims) ReadNodeClaim(_ context.Context, workspace, name string) (*v1alpha1.NodeClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.readErr != nil {
		return nil, f.readErr
	}
	claim, ok := f.claims[f.key(workspace, name)]
	if !ok {
		return nil, nil
	}
	out := claim
	return &out, nil
}

func (f *fakeClaims) ListNodeClaims(_ context.Context, workspace string) ([]v1alpha1.NodeClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []v1alpha1.NodeClaim{}
	for key, claim := range f.claims {
		if strings.HasPrefix(key, workspace+"/") {
			out = append(out, claim)
		}
	}
	return out, nil
}

func (f *fakeClaims) DeleteNodeClaim(_ context.Context, workspace, name string) error {
	f.mu.Lock()
	f.deletes++
	if f.deleteErr != nil {
		f.mu.Unlock()
		return f.deleteErr
	}
	key := f.key(workspace, name)
	claim, ok := f.claims[key]
	if !ok {
		f.mu.Unlock()
		return nil
	}
	if !slices.Contains(claim.Finalizers, v1alpha1.FinalizerNodeClaim) {
		delete(f.claims, key)
		f.mu.Unlock()
		return nil
	}
	now := metav1.Now()
	claim.DeletionTimestamp = &now
	f.claims[key] = claim
	release := f.release
	f.mu.Unlock()
	if release != nil {
		release(key)
	}
	return nil
}

func (f *fakeClaims) setPhase(workspace, name string, phase v1alpha1.NodeClaimPhase, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claim := f.claims[f.key(workspace, name)]
	claim.Status.Phase = phase
	claim.Status.Message = message
	f.claims[f.key(workspace, name)] = claim
}

func (f *fakeClaims) setFinalizer(workspace, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claim := f.claims[f.key(workspace, name)]
	claim.Finalizers = []string{v1alpha1.FinalizerNodeClaim}
	f.claims[f.key(workspace, name)] = claim
}

func (f *fakeClaims) doorDestroysTheVM(workspace, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.claims, f.key(workspace, name))
}

func (f *fakeClaims) held() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.claims)
}

func (f *fakeClaims) at(workspace, name string) v1alpha1.NodeClaim {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims[f.key(workspace, name)]
}

func (f *fakeClaims) deleted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

const (
	testWorkspace = "root:acct:home"
	testOwnerDid  = "did:web:example.com"
	testNodeName  = "liveweb-node1"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testOptions(t *testing.T, claims Claims) Options {
	t.Helper()
	dir := t.TempDir()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("join-token"), 0o600); err != nil {
		t.Fatalf("writing the token: %v", err)
	}
	ca := filepath.Join(t.TempDir(), "server-ca.crt")
	if err := os.WriteFile(ca, []byte("server-ca"), 0o600); err != nil {
		t.Fatalf("writing the CA: %v", err)
	}
	return Options{
		Cluster:          "liveweb",
		NodeName:         testNodeName,
		AdvertiseAddress: "100.127.255.254",
		ApiserverPort:    24000,
		TokenFile:        token,
		ServerCAFile:     ca,
		DataDir:          filepath.Join(dir, "nodes", testNodeName),
		Resolve: func(context.Context) (Cluster, error) {
			return Cluster{Workspace: testWorkspace, OwnerDid: testOwnerDid}, nil
		},
		Claims: claims,
		Record: func(context.Context, string, v1alpha1.NodePhase) error { return nil },
		Log:    testLogger(),
	}
}

type recorder struct {
	mu     sync.Mutex
	phases []v1alpha1.NodePhase
}

func (r *recorder) record(_ context.Context, _ string, phase v1alpha1.NodePhase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phases = append(r.phases, phase)
	return nil
}

func (r *recorder) seen() []v1alpha1.NodePhase {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]v1alpha1.NodePhase(nil), r.phases...)
}

func TestProvisionWritesTheClaimAndReturnsWithoutWaitingForTheMarket(t *testing.T) {
	claims := &fakeClaims{}
	opts := testOptions(t, claims)
	rec := &recorder{}
	opts.Record = rec.record
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Provision(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Provision did not return. The scale pass runs every 2 s for every cluster and a " +
			"market node takes minutes to come up, so a Provision that waits is a gateway that " +
			"stops deciding anything else while it waits")
	}

	claim := claims.at(testWorkspace, testNodeName)
	if claim.Spec.Cluster != "liveweb" || claim.Spec.NodeName != testNodeName {
		t.Fatalf("the claim does not name the cluster and node it is for: %+v", claim.Spec)
	}
	if claim.Spec.OwnerDid != testOwnerDid {
		t.Fatalf("the claim names owner %q, want the cluster's own account %q: the door fulfils "+
			"the claim through that owner's OAuth session, so a wrong DID is a claim no session "+
			"can act for", claim.Spec.OwnerDid, testOwnerDid)
	}
	if claim.Spec.AdvertiseAddress != "100.127.255.254" || claim.Spec.ApiserverPort != 24000 {
		t.Fatalf("the claim does not carry the address and port the node dials: %+v", claim.Spec)
	}
	if claim.Spec.TokenFile != opts.TokenFile || claim.Spec.ServerCAFile != opts.ServerCAFile {
		t.Fatalf("the claim does not carry the paths the door reads the join material from: %+v",
			claim.Spec)
	}
	wantPassword := nodevm.NodePasswordPath(opts.DataDir)
	if claim.Spec.NodePasswordFile != wantPassword {
		t.Fatalf("the claim names %q as the node password file, want %q",
			claim.Spec.NodePasswordFile, wantPassword)
	}
	if _, err := os.Stat(wantPassword); err != nil {
		t.Fatalf("the node password the claim names was never minted: %v -- the door reads that "+
			"file, so a claim naming a path nothing wrote is one the door can only fail on", err)
	}
	if got := rec.seen(); len(got) != 1 || got[0] != v1alpha1.NodePending {
		t.Fatalf("the Node CR saw phases %v, want exactly one Pending written before the claim "+
			"existed", got)
	}
	if claim.Status.Phase != "" {
		t.Fatalf("the claim was written with status %q. Status is the door's alone: a writer that "+
			"sets it makes its own claim look fulfilled", claim.Status.Phase)
	}
}

func TestASecondProvisionLeavesOneClaim(t *testing.T) {
	claims := &fakeClaims{}
	host, err := New(testOptions(t, claims))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := host.Provision(context.Background()); err != nil {
			t.Fatalf("Provision %d: %v", i+1, err)
		}
	}
	if got := claims.held(); got != 1 {
		t.Fatalf("two provisions left %d claims, want one: the scale pass runs every 2 s and a "+
			"market node takes minutes, so a claim per pass is a claim per two seconds", got)
	}
}

func TestConfirmJoinedNeedsTheGuestToHaveFetchedItsSecrets(t *testing.T) {
	claims := &fakeClaims{}
	opts := testOptions(t, claims)
	rec := &recorder{}
	opts.Record = rec.record
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	for _, early := range []v1alpha1.NodeClaimPhase{v1alpha1.NodeClaimBidding, v1alpha1.NodeClaimProvisioning} {
		claims.setPhase(testWorkspace, testNodeName, early, "")
		if err := host.ConfirmJoined(context.Background()); err == nil {
			t.Fatalf("a claim still %s was confirmed as joined: the door writes Joining only once "+
				"the guest fetched this claim's secrets, and no other machine can hold the node "+
				"password the agent registers with, so a node reported before that is something "+
				"else registered under this name", early)
		}
	}

	for _, joined := range []v1alpha1.NodeClaimPhase{v1alpha1.NodeClaimJoining, v1alpha1.NodeClaimRunning} {
		claims.setPhase(testWorkspace, testNodeName, joined, "")
		if err := host.ConfirmJoined(context.Background()); err != nil {
			t.Fatalf("ConfirmJoined on a %s claim: %v -- the door no longer writes Running, so a "+
				"Joining claim whose node the cluster reports is the joined node", joined, err)
		}
	}
	phases := rec.seen()
	if len(phases) < 2 || phases[0] != v1alpha1.NodePending || phases[1] != v1alpha1.NodeRunning {
		t.Fatalf("the Node CR saw %v, want Pending then Running", phases)
	}
}

func TestFatalReturnsTheDoorsOwnWords(t *testing.T) {
	claims := &fakeClaims{}
	host, err := New(testOptions(t, claims))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := host.Terminal(); got != "" {
		t.Fatalf("a pending claim reported %q as fatal, which ends a join wait that has not failed", got)
	}
	claims.setPhase(testWorkspace, testNodeName, v1alpha1.NodeClaimFailed, "no bidder under the cap")
	got := host.Terminal()
	if !strings.Contains(got, "no bidder under the cap") {
		t.Fatalf("Fatal returned %q, want the door's own message: a join wait that times out over a "+
			"claim that failed at its first step reports the timeout instead of the cause", got)
	}
}

func TestAFailedClaimIsNotConfirmedAsJoined(t *testing.T) {
	claims := &fakeClaims{}
	host, err := New(testOptions(t, claims))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.setPhase(testWorkspace, testNodeName, v1alpha1.NodeClaimFailed, "the winning bid was rejected by policy")
	err = host.ConfirmJoined(context.Background())
	if err == nil {
		t.Fatal("a failed claim was confirmed as joined")
	}
	if !strings.Contains(err.Error(), "the winning bid was rejected by policy") {
		t.Fatalf("the refusal does not carry the door's words: %v", err)
	}
}

func TestTeardownClosesTheClaimAndWaitsForTheDoorToDestroyTheMachine(t *testing.T) {
	claims := &fakeClaims{}
	opts := testOptions(t, claims)
	opts.ReleaseTimeout = 5 * time.Second
	opts.Poll = time.Millisecond
	rec := &recorder{}
	opts.Record = rec.record
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.setFinalizer(testWorkspace, testNodeName)
	claims.setPhase(testWorkspace, testNodeName, v1alpha1.NodeClaimRunning, "")
	claims.release = func(key string) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			claims.doorDestroysTheVM(testWorkspace, testNodeName)
		}()
	}
	if err := host.Teardown(ctx); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if got := claims.held(); got != 0 {
		t.Fatalf("%d claims survived the teardown", got)
	}
	phases := rec.seen()
	if len(phases) != 2 || phases[1] != v1alpha1.NodeDraining {
		t.Fatalf("the Node CR saw %v, want the Draining phase before the claim was closed", phases)
	}
}

func TestATeardownTheDoorNeverReleasesIsNotReportedAsDone(t *testing.T) {
	claims := &fakeClaims{}
	opts := testOptions(t, claims)
	opts.ReleaseTimeout = 40 * time.Millisecond
	opts.Poll = 5 * time.Millisecond
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.setFinalizer(testWorkspace, testNodeName)
	err = host.Teardown(ctx)
	if err == nil {
		t.Fatal("a claim the door never released was reported as a completed teardown. The door " +
			"removes its finalizer only once the market VM is gone, so this is a machine still " +
			"being paid for, and closing is not destroying")
	}
	if !strings.Contains(err.Error(), "still there") {
		t.Fatalf("the refusal does not say what it waited for: %v", err)
	}
	if got := claims.deleted(); got != 1 {
		t.Fatalf("the claim was deleted %d times, want exactly one", got)
	}
}

func TestAReleaseReadThatFailsIsNotAClaimThatIsGone(t *testing.T) {
	claims := &fakeClaims{}
	opts := testOptions(t, claims)
	opts.ReleaseTimeout = 40 * time.Millisecond
	opts.Poll = 5 * time.Millisecond
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := host.Provision(ctx); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.readErr = errors.New("the apiserver is unreachable")
	err = host.Teardown(ctx)
	if err == nil {
		t.Fatal("a teardown whose reads all failed was reported as done: a read that failed is not " +
			"a claim that is gone, and reporting a release nobody observed is how a machine that " +
			"is still running reads as closed")
	}
}

func TestAnIncompleteClaimIsRefusedRatherThanWritten(t *testing.T) {
	claims := &fakeClaims{}
	opts := testOptions(t, claims)
	opts.Claims = nil
	host, err := New(opts)
	if err == nil {
		t.Fatal("a market host was built with no store to write a claim through: it would count a " +
			"node it never asked anybody to rent")
	}
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("the refusal is %v, want ErrIncomplete", err)
	}
	if host != nil {
		t.Fatal("a host was returned alongside the refusal")
	}
}

func TestAWriteThatFailedLeavesNoClaimAndNamesTheNode(t *testing.T) {
	claims := &fakeClaims{writeErr: errors.New("kcp refused the write")}
	host, err := New(testOptions(t, claims))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = host.Provision(context.Background())
	if err == nil {
		t.Fatal("a claim write that failed was reported as a provisioned node")
	}
	if !strings.Contains(err.Error(), testNodeName) || !strings.Contains(err.Error(), "kcp refused the write") {
		t.Fatalf("the failure does not name the node and the cause: %v", err)
	}
	if got := claims.held(); got != 0 {
		t.Fatalf("%d claims exist after a write that failed", got)
	}
}
