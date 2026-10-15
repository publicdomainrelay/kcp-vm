package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

type fakeDeny struct {
	state revocation.DenyState
	err   error
	reads int
}

func (f *fakeDeny) Lookup(context.Context, string) (revocation.DenyState, error) {
	f.reads++
	return f.state, f.err
}

type fakeStore struct {
	views    map[string]ClusterView
	creates  int
	lastName string
	ttl      time.Duration

	calls []string

	afterEnsure func()
	ensureErr   error
}

func newStore() *fakeStore { return &fakeStore{views: map[string]ClusterView{}} }

func (s *fakeStore) Ensure(_ context.Context, caller Caller, name string) error {
	s.calls = append(s.calls, "ensure")
	s.creates++
	s.lastName = name
	if s.ensureErr != nil {
		return s.ensureErr
	}
	if _, ok := s.views[name]; !ok {
		s.views[name] = ClusterView{Name: name, Phase: "Initializing", Instance: "1"}
	}
	if s.afterEnsure != nil {
		s.afterEnsure()
	}
	return nil
}

func (s *fakeStore) Kubeconfig(_ context.Context, _ Caller, name string, ttl time.Duration, _ bool) (string, error) {
	s.ttl = ttl
	return "server: https://kcp.invalid/clusters/root:abc:def:" + name, nil
}

func (s *fakeStore) Get(ctx context.Context, _ Caller, name string) (ClusterView, bool, error) {
	s.calls = append(s.calls, "get")
	if err := ctx.Err(); err != nil {
		return ClusterView{}, false, err
	}
	v, ok := s.views[name]
	return v, ok, nil
}

func (s *fakeStore) List(_ context.Context, _ Caller) ([]ClusterView, error) {
	out := make([]ClusterView, 0, len(s.views))
	for _, v := range s.views {
		out = append(out, v)
	}
	return out, nil
}

func (s *fakeStore) SetRunning(ctx context.Context, _ Caller, name string, running bool) (ClusterView, error) {
	s.calls = append(s.calls, "setRunning")
	if err := ctx.Err(); err != nil {
		return ClusterView{}, err
	}
	v, ok := s.views[name]
	if !ok {
		return ClusterView{}, ErrClusterNotFound
	}
	v.Running = running
	if !running {
		v.Phase = "Unavailable"
	}
	s.views[name] = v
	return v, nil
}

func (s *fakeStore) Delete(_ context.Context, _ Caller, name string) (ClusterView, error) {
	v, ok := s.views[name]
	if !ok {
		return ClusterView{}, ErrClusterNotFound
	}
	v.Phase = "Draining"
	delete(s.views, name)
	return v, nil
}

func server(state revocation.DenyState) (*Server, *fakeDeny, *fakeStore) {
	d := &fakeDeny{state: state}
	s := newStore()
	return &Server{Deny: d, Store: s}, d, s
}

func TestRevokedDIDIsRefusedAndNothingIsCreated(t *testing.T) {
	srv, deny, store := server(revocation.DenyRevoked)

	_, err := srv.GetKubeconfig(context.Background(), "did:plc:revoked", KubeconfigRequest{Name: "web"})
	if err == nil {
		t.Fatal("a REVOKED identity got a kubeconfig -- and because getKubeconfig is " +
			"create-on-read, that call also REBUILDS everything the revocation deleted")
	}
	if !errors.Is(err, ErrIdentityRevoked) {
		t.Fatalf("want IdentityRevoked, got %v", err)
	}
	if store.creates != 0 {
		t.Fatalf("the store was written %d times for a revoked identity -- THE DENY MUST "+
			"BE CONSULTED BEFORE ANYTHING IS CREATED", store.creates)
	}
	if deny.reads == 0 {
		t.Fatal("the deny was never consulted")
	}
}

func TestCreateClusterIsRefusedForARevokedIdentityAndCreatesNothing(t *testing.T) {
	srv, deny, store := server(revocation.DenyRevoked)

	_, err := srv.CreateCluster(context.Background(), "did:plc:revoked", NameRequest{Name: "web"})
	if !errors.Is(err, ErrIdentityRevoked) {
		t.Fatalf("want IdentityRevoked, got %v", err)
	}
	if store.creates != 0 {
		t.Fatalf("createCluster wrote the store %d time(s) for a revoked identity -- it walks "+
			"the same create-on-read path getKubeconfig does, and that path must consult the "+
			"deny BEFORE it creates anything", store.creates)
	}
	if deny.reads == 0 {
		t.Fatal("the deny was never consulted")
	}
}

func TestTheCallerIsNotReadFromTheRequest(t *testing.T) {
	srv, _, _ := server(revocation.DenyGranted)
	if _, err := srv.GetKubeconfig(context.Background(), "", KubeconfigRequest{Name: "web"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an empty caller must be refused, got %v", err)
	}
}

func TestOnlyTheCreatingVerbsCreate(t *testing.T) {
	srv, _, store := server(revocation.DenyGranted)
	store.views["web"] = ClusterView{Name: "web", Phase: "Ready", URL: "https://x", Instance: "1"}

	ctx := context.Background()
	if _, err := srv.ControlPlaneUp(ctx, "did:plc:a", NameRequest{Name: "web"}); err != nil {
		t.Fatalf("ControlPlaneUp: %v", err)
	}
	if _, err := srv.ControlPlaneDown(ctx, "did:plc:a", NameRequest{Name: "web"}); err != nil {
		t.Fatalf("ControlPlaneDown: %v", err)
	}
	if _, err := srv.ListClusters(ctx, "did:plc:a"); err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if store.creates != 0 {
		t.Fatalf("the non-creating lifecycle verbs created %d times; every one of them must "+
			"be a pure read or update", store.creates)
	}

	if _, err := srv.CreateCluster(ctx, "did:plc:a", NameRequest{Name: "web"}); err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if store.creates != 1 {
		t.Fatalf("CreateCluster created %d times, want exactly 1: it walks getKubeconfig's "+
			"create-on-read path and then flips the desired state", store.creates)
	}
	if v := store.views["web"]; !v.Running {
		t.Fatalf("CreateCluster left the cluster not running: %+v", v)
	}
	if _, err := srv.CreateCluster(ctx, "did:plc:a", NameRequest{Name: "web"}); err != nil {
		t.Fatalf("a second CreateCluster: %v", err)
	}
	if store.creates != 2 {
		t.Fatalf("a second CreateCluster reached Ensure %d times in total; the tree half is "+
			"create-on-read and is expected to run again, and the desired-state half must "+
			"still be idempotent", store.creates)
	}
}

func TestCreateClusterRecordsRunningBeforeItReadsAnythingBack(t *testing.T) {
	srv, _, store := server(revocation.DenyGranted)

	if _, err := srv.CreateCluster(context.Background(), "did:plc:a", NameRequest{Name: "web"}); err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if got := store.calls; len(got) != 2 || got[0] != "ensure" || got[1] != "setRunning" {
		t.Fatalf("CreateCluster called the store %v, want [ensure setRunning]. The desired state "+
			"is recorded first and nothing stands between the two: a create that reads the phase "+
			"back (or waits for status.URL) before it writes running=true is a create that "+
			"returns with running=false when the door aborts it at its 30 s budget, and nothing "+
			"ever sets that flag afterwards -- the provider brings a cluster's control plane up "+
			"only while spec.running is true", got)
	}
	if v := store.views["web"]; !v.Running {
		t.Fatalf("CreateCluster left the cluster not running: %+v", v)
	}
}

func TestACreateTheCallerAbandonedStillLeavesTheClusterRunning(t *testing.T) {
	srv, _, store := server(revocation.DenyGranted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.afterEnsure = cancel

	if _, err := srv.CreateCluster(ctx, "did:plc:a", NameRequest{Name: "web"}); err != nil {
		t.Fatalf("CreateCluster after the caller hung up mid-create: %v. Ensure already wrote "+
			"the tree, so the one thing left to record is the intent to run it", err)
	}
	if v := store.views["web"]; !v.Running {
		t.Fatalf("a create the caller abandoned left the cluster not running: %+v. The tree "+
			"exists and the flag does not, which is the production shape: the SPA's cluster "+
			"stays down for ever because no pass brings up a control plane whose spec.running "+
			"is false", v)
	}
}

func TestCreateReportsThePhaseItFoundRatherThanRefusing(t *testing.T) {
	srv, _, store := server(revocation.DenyGranted)
	store.views["web"] = ClusterView{Name: "web", Phase: "Initializing", Instance: "1"}

	view, err := srv.CreateCluster(context.Background(), "did:plc:a", NameRequest{Name: "web"})
	if err != nil {
		t.Fatalf("CreateCluster refused a cluster whose control plane is still coming up: %v. "+
			"A market join is minutes away by construction, so a create that waits for the "+
			"apiserver's URL is a create the door aborts, and the abort is what dropped the "+
			"running write", err)
	}
	if view.Phase != "Initializing" {
		t.Fatalf("the create reported phase %q, want the phase the CR carries: the caller "+
			"needs the state, not an error that reads like the create did not happen", view.Phase)
	}
	if !view.Running {
		t.Fatalf("the create reported %+v: the desired state must be set even while the "+
			"control plane is still coming up", view)
	}
}

func TestACreateStillRefusesADrainingCluster(t *testing.T) {
	srv, _, _ := server(revocation.DenyGranted)
	store := srv.Store.(*fakeStore)
	store.views["draining"] = ClusterView{Name: "draining", Phase: "Draining"}

	if _, err := srv.CreateCluster(context.Background(), "did:plc:a", NameRequest{Name: "draining"}); !errors.Is(err, ErrClusterDraining) {
		t.Fatalf("want ClusterDraining, got %v: Draining is a one-way ratchet and a create "+
			"must not read as one that will serve", err)
	}
}

func TestAClusterTheCallerDoesNotOwnIs404NotForbidden(t *testing.T) {
	srv, _, _ := server(revocation.DenyGranted)
	_, err := srv.ControlPlaneUp(context.Background(), "did:plc:a", NameRequest{Name: "someone-elses"})
	if !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("want ClusterNotFound, got %v -- 403 would disclose that the name exists", err)
	}
	if ErrClusterNotFound.Status != 404 {
		t.Fatalf("ClusterNotFound must map to 404, got %d", ErrClusterNotFound.Status)
	}
}

func TestDrainingIsNotRetryableAndInitializingIs(t *testing.T) {
	if ErrClusterDraining.Status == 429 {
		t.Fatal("Draining must NOT be retryable: it is a one-way ratchet")
	}
	if ErrClusterInitializing.Status != 429 {
		t.Fatalf("Initializing must be 429 with Retry-After, got %d", ErrClusterInitializing.Status)
	}

	srv, _, store := server(revocation.DenyGranted)
	store.views["draining"] = ClusterView{Name: "draining", Phase: "Draining"}
	if _, err := srv.ControlPlaneUp(context.Background(), "did:plc:a", NameRequest{Name: "draining"}); !errors.Is(err, ErrClusterDraining) {
		t.Fatalf("want ClusterDraining, got %v", err)
	}

	store.views["init"] = ClusterView{Name: "init", Phase: "Initializing"}
	if _, err := srv.ControlPlaneUp(context.Background(), "did:plc:a", NameRequest{Name: "init"}); !errors.Is(err, ErrClusterInitializing) {
		t.Fatalf("want ClusterInitializing, got %v", err)
	}
}

const unprivilegedRefusal = `workspaces.tenancy.kcp.io "clusters" is forbidden: ` +
	"spec.URL can only be set by system privileged users"

func TestAnEnsureFailureKeepsItsOwnCauseAndIsLogged(t *testing.T) {
	srv, _, store := server(revocation.DenyGranted)
	store.ensureErr = fmt.Errorf("kcpclient: mounted child root:abc:web:clusters: %s",
		unprivilegedRefusal)

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	_, err := srv.CreateCluster(context.Background(), "did:plc:a", NameRequest{Name: "web"})
	var xe *XRPCError
	if !errors.As(err, &xe) {
		t.Fatalf("want a named XRPC error, got %v", err)
	}
	if xe.Name != ErrClusterUnavailable.Name || xe.Status != ErrClusterUnavailable.Status {
		t.Fatalf("a kcp refusal that is not a revocation answered %s/%d, want %s/%d",
			xe.Name, xe.Status, ErrClusterUnavailable.Name, ErrClusterUnavailable.Status)
	}
	if !strings.Contains(xe.Message, "could not be prepared") {
		t.Fatalf("the message does not say the cluster could not be prepared: %q", xe.Message)
	}
	if !strings.Contains(xe.Message, "spec.URL can only be set by system privileged users") {
		t.Fatalf("the message hides the cause: %q. That sentence IS the diagnosis an operator "+
			"needs, and the previous mapping replaced it with a claim about the identity store",
			xe.Message)
	}
	if strings.Contains(xe.Message, ErrClusterUnavailable.Message) {
		t.Fatalf("a kcp refusal answered %q, so the caller reads the identity store as the "+
			"culprit for a write that was refused on privilege", xe.Message)
	}
	if !strings.Contains(logged.String(), "spec.URL can only be set by system privileged users") {
		t.Fatalf("the full error was not logged: %q", logged.String())
	}
}

func TestAnUnreadableDenyStillReadsAsTheIdentityStore(t *testing.T) {
	d := &fakeDeny{err: fmt.Errorf("kcp unreachable")}
	store := newStore()
	srv := &Server{Deny: d, Store: store}

	_, err := srv.CreateCluster(context.Background(), "did:plc:a", NameRequest{Name: "web"})
	var xe *XRPCError
	if !errors.As(err, &xe) {
		t.Fatalf("want a named XRPC error, got %v", err)
	}
	if xe.Message != ErrClusterUnavailable.Message {
		t.Fatalf("a deny that could not be read answered %q, want %q: the two failures must "+
			"not read identically, or the fix for one hides the other", xe.Message,
			ErrClusterUnavailable.Message)
	}
	if store.creates != 0 {
		t.Fatalf("the store was written %d times while the deny was unreadable", store.creates)
	}
}

func TestAnUnreadableDenyFailsClosed(t *testing.T) {
	d := &fakeDeny{err: fmt.Errorf("kcp unreachable")}
	store := newStore()
	srv := &Server{Deny: d, Store: store}

	_, err := srv.GetKubeconfig(context.Background(), "did:plc:a", KubeconfigRequest{Name: "web"})
	if err == nil {
		t.Fatal("an unreadable deny ADMITTED the caller -- fail closed means refuse")
	}
	if store.creates != 0 {
		t.Fatalf("the store was written %d times while the deny was unreadable", store.creates)
	}
}

func TestAMissingRecordIsDistinctFromARevocation(t *testing.T) {
	srv, _, _ := server(revocation.DenyRecordMissing)
	_, err := srv.GetKubeconfig(context.Background(), "did:plc:a", KubeconfigRequest{Name: "web"})
	if err == nil {
		t.Fatal("a MISSING identity record was admitted -- deleting the record must not " +
			"buy a fresh first login, which is the same re-arming one level up")
	}
	var xe *XRPCError
	if !errors.As(err, &xe) {
		t.Fatalf("want a named XRPC error, got %v", err)
	}
	if xe.Message == ErrIdentityRevoked.Message {
		t.Fatal("a missing record and a revocation must not read identically: the causes " +
			"are different and an operator needs to know which happened")
	}
}
