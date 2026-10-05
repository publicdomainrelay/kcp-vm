//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

type fakeClusters struct {
	items  []v1alpha1.Cluster
	writes int
	refs   int

	removeFinalizers int
}

func (f *fakeClusters) Refs(context.Context) ([]clusterprovider.Ref, error) {
	f.refs++
	out := make([]clusterprovider.Ref, 0, len(f.items))
	for _, c := range f.items {
		out = append(out, clusterprovider.Ref{Home: "root:acct:home", Name: c.Name})
	}
	return out, nil
}

func (f *fakeClusters) Read(_ context.Context, ref clusterprovider.Ref) (*v1alpha1.Cluster, error) {
	for i := range f.items {
		if f.items[i].Name == ref.Name {
			return &f.items[i], nil
		}
	}
	return nil, errors.New("no such cluster")
}

func (f *fakeClusters) RemoveFinalizer(context.Context, clusterprovider.Ref) error {
	f.removeFinalizers++
	return nil
}

func (f *fakeClusters) WriteStatus(context.Context, clusterprovider.Ref, v1alpha1.ClusterStatus) error {
	f.writes++
	return nil
}

func (f *fakeClusters) MountStatus(context.Context, clusterprovider.Ref) int { return 0 }

func (f *fakeClusters) IdentityState(context.Context, clusterprovider.Ref) (v1alpha1.IdentityState, error) {
	return "", nil
}

func (f *fakeClusters) Nodes(context.Context, clusterprovider.Ref) ([]v1alpha1.Node, error) {
	return nil, nil
}

func (f *fakeClusters) WriteNode(context.Context, clusterprovider.Ref, string, v1alpha1.NodeStatus) error {
	return nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testAllocator(t *testing.T) *portallocator.Allocator {
	t.Helper()
	a, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "ports.json"),
		Range:     portallocator.Range{First: 20000, Last: 20099},
		Ephemeral: &portallocator.Range{First: 49152, Last: 65535},
		Probe:     func(int) bool { return false },
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	return a
}

func testNetworks(t *testing.T) *subnetallocator.Allocator {
	t.Helper()
	n, err := subnetallocator.Open(subnetallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "networks.json"),
	})
	if err != nil {
		t.Fatalf("subnetallocator.Open: %v", err)
	}
	return n
}

var errTestNoSteps = errors.New("wiring_test: this decider has no steps")

func decider(t *testing.T, a *portallocator.Allocator) *materialiser.Reconciler {
	t.Helper()
	rec, err := materialiser.New(materialiser.Options{
		Host:     materialiser.NewLocalHost(procgroup.Options{}),
		Ports:    a,
		Networks: testNetworks(t),
		Steps: func(materialiser.Observed, materialiser.Leases) ([]procgroup.Step, error) {
			return nil, errTestNoSteps
		},
		Identity:    func(context.Context, materialiser.Observed) error { return errTestNoSteps },
		NewInstance: instanceMinter(),
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}
	return rec
}

func testDeps(t *testing.T, f *fakeClusters) Deps {
	t.Helper()
	a := testAllocator(t)
	return Deps{
		Registry:    f,
		Decider:     decider(t, a),
		Allocator:   a,
		NewInstance: func() string { return "gen-test-1" },
		Tenant: func(context.Context, clusterprovider.Ref, *v1alpha1.Cluster) (*rest.Config, error) {
			return &rest.Config{Host: "https://127.0.0.1:16445"}, nil
		},
		Host: materialiser.NewLocalHost(procgroup.Options{}),
	}
}

func TestObserveModeWritesNothing(t *testing.T) {
	f := &fakeClusters{items: []v1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: "liveweb"},
		Spec:       v1alpha1.ClusterSpec{Account: "did:web:example.com", Running: false},
		Status:     v1alpha1.ClusterStatus{Phase: readyPhase(t), URL: "https://192.168.5.2:16445"},
	}}}
	cfg := Config{LeasePath: filepath.Join(t.TempDir(), "ports.json"), GrantPath: DefaultGrantPath}
	p, err := Build(cfg, testDeps(t, f), testLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !cfg.ObserveOnly() {
		t.Fatal("an unset Join must be observe-only")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = p.Run(ctx)

	if f.writes != 0 {
		t.Errorf("%d status writes in observe-only mode: a pass that may not act must not "+
			"write either -- a live CR's phase would move on an observation alone", f.writes)
	}
	if f.refs == 0 {
		t.Error("the observe loop never listed: it is meant to decide and REPORT, not to idle")
	}
}

func TestBuildRefusesWhatItCannotDo(t *testing.T) {
	f := &fakeClusters{}
	full := testDeps(t, f)
	cfg := Config{LeasePath: filepath.Join(t.TempDir(), "ports.json"), GrantPath: DefaultGrantPath}

	for _, tc := range []struct {
		name string
		zero func(*Deps)
		join string
		want error
	}{
		{"no registry", func(d *Deps) { d.Registry = nil }, "", nil},
		{"no decider", func(d *Deps) { d.Decider = nil }, "", clusterprovider.ErrNoDecider},
		{"no allocator", func(d *Deps) { d.Allocator = nil }, "", nil},
		{"no tenant", func(d *Deps) { d.Tenant = nil }, "liveweb", ErrNoTenant},
		{"join without a process host", func(d *Deps) { d.Host = nil }, "liveweb", ErrNoHost},
	} {
		d := full
		tc.zero(&d)
		c := cfg
		c.Join = tc.join
		_, err := Build(c, d, testLogger())
		if err == nil {
			t.Errorf("%s: Build accepted it", tc.name)
			continue
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}

	d := full
	d.Host, d.Tenant = nil, nil
	if _, err := Build(cfg, d, testLogger()); err != nil {
		t.Errorf("observe-only Build refused without a Host or a Tenant: %v", err)
	}
}

func TestJoinRefusesAnUnknownClusterAndNamesTheTreeItSearched(t *testing.T) {
	f := &fakeClusters{items: []v1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: "liveweb"},
		Spec:       v1alpha1.ClusterSpec{Account: "did:web:example.com"},
	}}}
	cfg := Config{
		LeasePath:       filepath.Join(t.TempDir(), "ports.json"),
		GrantPath:       DefaultGrantPath,
		MintKubeconfigs: t.TempDir(),
		Join:            "nosuch",
	}
	d := testDeps(t, f)
	d.Host = materialiser.NewLocalHost(procgroup.Options{})
	p, err := Build(cfg, d, testLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	err = p.Run(context.Background())
	if err == nil {
		t.Fatal("a join for a cluster that does not exist succeeded")
	}
	for _, want := range []string{"nosuch", "root:acct:home/liveweb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if f.writes != 0 {
		t.Error("a failed join wrote a status")
	}
}

func TestLoadRefusesAHalfConfiguredJoin(t *testing.T) {
	env := map[string]string{
		"SOCIALWEB_KCP_HOST":                   "https://127.0.0.1:6443",
		"SOCIALWEB_KCP_KUBECONFIG":             "/tmp/live-kcp.yaml",
		"SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH": "/tmp/ports.json",
	}
	get := func(k string) string { return env[k] }

	cfg, err := load(get)
	if err != nil {
		t.Fatalf("observe-only load: %v", err)
	}
	if !cfg.ObserveOnly() || cfg.GrantPath != DefaultGrantPath {
		t.Errorf("observe-only load = %+v", cfg)
	}

	env["SOCIALWEB_CLUSTERPROVIDER_JOIN"] = "liveweb"
	if _, err := load(get); err == nil || !strings.Contains(err.Error(), "SOCIALWEB_GATEWAY_MINT_KUBECONFIGS") {
		t.Errorf("a join with no mint directory: %v, want a refusal naming %s", err, "SOCIALWEB_GATEWAY_MINT_KUBECONFIGS")
	}

	env["SOCIALWEB_GATEWAY_MINT_KUBECONFIGS"] = t.TempDir()
	if _, err := load(get); err == nil || !strings.Contains(err.Error(), "DATASTORE_HAS_ROW") {
		t.Errorf("a join with no datastore observation: %v, want a refusal naming DATASTORE_HAS_ROW", err)
	}

	env["SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW"] = "maybe"
	if _, err := load(get); err == nil {
		t.Error("a datastore observation of \"maybe\" was accepted")
	}

	env["SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW"] = "true"

	if _, err := load(get); err == nil || !strings.Contains(err.Error(), "SOCIALWEB_CLUSTERPROVIDER_STATE_DIR") {
		t.Errorf("a join with no state dir and no datastore: %v, want a refusal naming STATE_DIR", err)
	}

	env["SOCIALWEB_CLUSTERPROVIDER_STATE_DIR"] = t.TempDir()
	env["SOCIALWEB_CLUSTERPROVIDER_CLOUD_CONTROLLER_MANAGER"] = "/usr/local/bin/socialweb-cloud-controller-manager"
	for k, v := range placementEnv(t) {
		env[k] = v
	}
	cfg, err = load(get)
	if err != nil {
		t.Fatalf("a fully configured join: %v", err)
	}
	if cfg.ObserveOnly() || !cfg.DatastoreHasRow {
		t.Errorf("a fully configured join = %+v", cfg)
	}
	if cfg.StateDir == "" {
		t.Error("the state dir was not carried into the Config")
	}

	delete(env, "SOCIALWEB_CLUSTERPROVIDER_STATE_DIR")
	env["SOCIALWEB_CLUSTERPROVIDER_DATASTORE"] = "postgres://postgres@127.0.0.1:25432/k3s?sslmode=disable"
	if _, err := load(get); err == nil || !strings.Contains(err.Error(), "SOCIALWEB_CLUSTERPROVIDER_STATE_DIR") {
		t.Errorf("an operator-supplied datastore without a state dir: %v, want a refusal naming "+
			"STATE_DIR. The store is not the only thing a cluster keeps on disk -- the node record, "+
			"the instance token, the audit log and the identity bundle are all under it, and a "+
			"gateway that restarts cannot reap what it cannot find", err)
	}

	env["SOCIALWEB_CLUSTERPROVIDER_STATE_DIR"] = t.TempDir()
	cfg, err = load(get)
	if err != nil {
		t.Fatalf("a join against an operator-supplied store: %v", err)
	}
	if cfg.StoreEndpoint != env["SOCIALWEB_CLUSTERPROVIDER_DATASTORE"] {
		t.Errorf("the operator's store did not reach Config.StoreEndpoint: %+v", cfg)
	}

	env["SOCIALWEB_CLUSTERPROVIDER_STORE_ENDPOINT"] = "t4://elsewhere"
	err = nil
	if _, err = load(get); err == nil {
		t.Error("two different store endpoints were resolved silently, and whichever lost is a " +
			"cluster whose objects went somewhere nobody is looking")
	} else if !strings.Contains(err.Error(), "STORE_ENDPOINT") || !strings.Contains(err.Error(), "DATASTORE") {
		t.Errorf("the refusal must name both settings so the operator knows which two disagree: %v", err)
	}
}
