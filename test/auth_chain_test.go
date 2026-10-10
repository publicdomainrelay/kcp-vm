package test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kubeconfig"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

var now = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

type denyStub struct {
	state revocation.DenyState
	err   error
	reads int
}

func (d *denyStub) Lookup(context.Context, string) (revocation.DenyState, error) {
	d.reads++
	return d.state, d.err
}

type storeStub struct {
	views   map[string]gateway.ClusterView
	creates int
	callers []gateway.Caller
}

func newStore() *storeStub { return &storeStub{views: map[string]gateway.ClusterView{}} }

func (s *storeStub) Ensure(_ context.Context, caller gateway.Caller, name string) error {
	s.creates++
	s.callers = append(s.callers, caller)
	if _, ok := s.views[name]; !ok {
		s.views[name] = gateway.ClusterView{Name: name, Phase: "Initializing", Instance: "1"}
	}
	return nil
}

func (s *storeStub) Kubeconfig(_ context.Context, caller gateway.Caller, name string, _ time.Duration) (string, error) {
	return kubeconfig.Build(kubeconfig.Input{
		ClusterName:      name,
		Server:           "https://kcp.invalid/clusters/root:abc:" + name + ":child",
		ExecCommand:      "/usr/local/bin/socialweb-credential",
		DeviceCredential: "header.payload.signature",
		DeviceKeyEnvName: "SOCIALWEB_DEVICE_KEY",
	})
}

func (s *storeStub) Get(_ context.Context, _ gateway.Caller, name string) (gateway.ClusterView, bool, error) {
	v, ok := s.views[name]
	return v, ok, nil
}

func (s *storeStub) List(context.Context, gateway.Caller) ([]gateway.ClusterView, error) {
	out := make([]gateway.ClusterView, 0, len(s.views))
	for _, v := range s.views {
		out = append(out, v)
	}
	return out, nil
}

func (s *storeStub) SetRunning(_ context.Context, _ gateway.Caller, name string, running bool) (gateway.ClusterView, error) {
	v, ok := s.views[name]
	if !ok {
		return gateway.ClusterView{}, gateway.ErrClusterNotFound
	}
	v.Running = running
	s.views[name] = v
	return v, nil
}

func (s *storeStub) Delete(_ context.Context, _ gateway.Caller, name string) (gateway.ClusterView, error) {
	v, ok := s.views[name]
	if !ok {
		return gateway.ClusterView{}, gateway.ErrClusterNotFound
	}
	delete(s.views, name)
	return v, nil
}

func TestTheVerifiedIdentityIsTheOneActedOn(t *testing.T) {
	deny := &denyStub{state: revocation.DenyGranted}
	store := newStore()
	srv := &gateway.Server{Deny: deny, Store: store}

	const verified = gateway.Caller("did:plc:verified")

	resp, err := srv.GetKubeconfig(context.Background(), verified, gateway.KubeconfigRequest{Name: "web"})
	if err != nil {
		t.Fatalf("GetKubeconfig: %v", err)
	}

	if len(store.callers) != 1 || store.callers[0] != verified {
		t.Fatalf("the store was handed %v; the identity acted on must be the VERIFIED one",
			store.callers)
	}
	if deny.reads == 0 {
		t.Fatal("the deny was never consulted, so a revoked identity would have been " +
			"admitted by the same call")
	}
	if err := kubeconfig.AssertClean(resp.Kubeconfig); err != nil {
		t.Fatalf("the kubeconfig reached the user unclean: %v", err)
	}
	if !strings.Contains(resp.Kubeconfig, "https://") {
		t.Fatalf("the kubeconfig's server is not https, so client-go will silently drop "+
			"every credential in it:\n%s", resp.Kubeconfig)
	}
}

func TestARefusedIdentityCreatesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state revocation.DenyState
		err   error
	}{
		{"revoked", revocation.DenyRevoked, nil},
		{"record missing is tampering, not a first login", revocation.DenyRecordMissing, nil},
		{"an unreadable deny fails closed", revocation.DenyGranted, errors.New("kcp unreachable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deny := &denyStub{state: tc.state, err: tc.err}
			store := newStore()
			srv := &gateway.Server{Deny: deny, Store: store}

			if _, err := srv.GetKubeconfig(context.Background(), "did:plc:a", gateway.KubeconfigRequest{Name: "web"}); err == nil {
				t.Fatal("a refused identity received a kubeconfig")
			}
			if store.creates != 0 {
				t.Fatalf("the store was written %d times for an identity that was refused: "+
					"CALLING getKubeconfig IS WHAT REBUILDS WHAT A REVOCATION DELETED",
					store.creates)
			}
		})
	}
}

func TestTheRenewalPathReConsultsRevocation(t *testing.T) {
	world := &mintWorld{saExists: false}
	m := mint.New(world, mint.Options{})

	cred, err := m.Issue(context.Background(), mint.DeviceCredential{Token: "valid-device-key"}, now)
	if err == nil {
		t.Fatalf("a token was minted for a DELETED ServiceAccount; the device credential is "+
			"long-lived and its validity MUST NOT matter: %+v", cred)
	}
	if !errors.Is(err, mint.ErrServiceAccountGone) {
		t.Fatalf("want ErrServiceAccountGone, got %v", err)
	}
	if world.issued != 0 {
		t.Fatalf("the mint issued %d times for a revoked identity", world.issued)
	}
}

type mintWorld struct {
	saExists bool
	issued   int
}

func (w *mintWorld) Resolve(context.Context, mint.DeviceCredential) (mint.Binding, error) {
	return mint.Binding{
		DID: "did:plc:a", Cluster: "web", SA: "did-web-example-a",
		SAExists: w.saExists, Instance: "1",
	}, nil
}

func (w *mintWorld) Issue(context.Context, mint.Binding, time.Duration) (string, time.Time, error) {
	w.issued++
	return "sa-token", now.Add(15 * time.Minute), nil
}
