package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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

func (s *fakeStore) Kubeconfig(_ context.Context, _ Caller, name string) (string, error) {
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

	_, err := srv.GetKubeconfig(context.Background(), "did:plc:revoked", NameRequest{Name: "web"})
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

func TestGrantedIdentityIsAdmittedAndCreatesOnFirstLogin(t *testing.T) {
	srv, deny, store := server(revocation.DenyGranted)

	resp, err := srv.GetKubeconfig(context.Background(), "did:plc:fresh", NameRequest{Name: "web"})
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

			resp, err := srv.GetKubeconfig(context.Background(), "did:plc:a", NameRequest{Name: "web"})
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
	if _, err := srv.GetKubeconfig(context.Background(), "", NameRequest{Name: "web"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an empty caller must be refused, got %v", err)
	}
}

func TestOnlyGetKubeconfigCreates(t *testing.T) {
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
		t.Fatalf("the lifecycle verbs created %d times; ONLY getKubeconfig is "+
			"create-on-read, and every other verb must be a pure read or update", store.creates)
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

	_, err := srv.GetKubeconfig(context.Background(), "did:plc:a", NameRequest{Name: "web"})
	if err == nil {
		t.Fatal("an unreadable deny ADMITTED the caller -- fail closed means refuse")
	}
	if store.creates != 0 {
		t.Fatalf("the store was written %d times while the deny was unreadable", store.creates)
	}
}

func TestAMissingRecordIsDistinctFromARevocation(t *testing.T) {
	srv, _, _ := server(revocation.DenyRecordMissing)
	_, err := srv.GetKubeconfig(context.Background(), "did:plc:a", NameRequest{Name: "web"})
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
