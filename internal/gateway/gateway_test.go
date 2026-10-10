package gateway

import (
	"context"
	"errors"
	"fmt"
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
}

func newStore() *fakeStore { return &fakeStore{views: map[string]ClusterView{}} }

func (s *fakeStore) Ensure(_ context.Context, caller Caller, name string) error {
	s.creates++
	s.lastName = name
	if _, ok := s.views[name]; !ok {
		s.views[name] = ClusterView{Name: name, Phase: "Initializing", Instance: "1"}
	}
	return nil
}

func (s *fakeStore) Kubeconfig(_ context.Context, _ Caller, name string, ttl time.Duration) (string, error) {
	s.ttl = ttl
	return "server: https://kcp.invalid/clusters/root:abc:def:" + name, nil
}

func (s *fakeStore) Get(_ context.Context, _ Caller, name string) (ClusterView, bool, error) {
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

func (s *fakeStore) SetRunning(_ context.Context, _ Caller, name string, running bool) (ClusterView, error) {
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

func TestGrantedIdentityIsAdmittedAndCreatesOnFirstLogin(t *testing.T) {
	srv, deny, store := server(revocation.DenyGranted)

	resp, err := srv.GetKubeconfig(context.Background(), "did:plc:fresh", KubeconfigRequest{Name: "web"})
	if err != nil {
		t.Fatalf("GetKubeconfig: %v", err)
	}
	if deny.reads != 1 {
		t.Fatalf("the deny was read %d times, want exactly 1", deny.reads)
	}
	if store.creates != 1 {
		t.Fatalf("creates = %d, want 1 (the create-on-read path)", store.creates)
	}
	if resp.Instance == "" {
		t.Fatal("the response carries no instance: a delete-then-recreate keeps the SAME " +
			"url string, so without it a cached kubeconfig silently re-points")
	}
}

func TestAColdClusterStillGetsAKubeconfig(t *testing.T) {
	for _, phase := range []string{"Initializing", "Unavailable", "Draining"} {
		t.Run(phase, func(t *testing.T) {
			srv, _, store := server(revocation.DenyGranted)
			store.views["web"] = ClusterView{Name: "web", Phase: phase, URL: "", Instance: "1"}

			resp, err := srv.GetKubeconfig(context.Background(), "did:plc:a", KubeconfigRequest{Name: "web"})
			if err != nil {
				t.Fatalf("getKubeconfig failed for a %s cluster: %v -- it must return a "+
					"kubeconfig, because the 503 comes from the MOUNT, not from this verb",
					phase, err)
			}
			if !strings.Contains(resp.Kubeconfig, "/clusters/") {
				t.Fatalf("the kubeconfig must point at the MOUNT path, not the backend: %q",
					resp.Kubeconfig)
			}
		})
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

func TestARequestedLifetimeOutsideTheConfiguredWindowIsRefused(t *testing.T) {
	seconds := func(n int64) *int64 { return &n }

	for _, tc := range []struct {
		name  string
		cap   time.Duration
		asked *int64
		want  time.Duration
	}{
		{"absent keeps the deployment's own", 2 * time.Hour, nil, 0},
		{"the declared floor", 2 * time.Hour, seconds(60), time.Minute},
		{"the configured ceiling", 2 * time.Hour, seconds(7200), 2 * time.Hour},
		{"one second below the floor", 2 * time.Hour, seconds(59), -1},
		{"one second above the ceiling", 2 * time.Hour, seconds(7201), -1},
		{"zero", 2 * time.Hour, seconds(0), -1},
		{"negative", 2 * time.Hour, seconds(-1), -1},
		{"no ceiling configured at all", 0, seconds(60), -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, store := server(revocation.DenyGranted)
			srv.MaxDeviceKeyTTL = tc.cap
			store.views["web"] = ClusterView{Name: "web", Phase: "Ready", Instance: "1"}

			resp, err := srv.GetKubeconfig(context.Background(), "did:plc:a",
				KubeconfigRequest{Name: "web", TTLSeconds: tc.asked})
			if tc.want < 0 {
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("want InvalidRequest, got %v (resp %+v)", err, resp)
				}
				if store.creates != 0 {
					t.Fatalf("a refused lifetime still ran the create path %d time(s): the "+
						"window is checked before anything is written", store.creates)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetKubeconfig: %v", err)
			}
			if store.ttl != tc.want {
				t.Fatalf("the store was handed lifetime %s, want %s", store.ttl, tc.want)
			}
		})
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
