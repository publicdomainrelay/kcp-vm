package main

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

type linkCall struct {
	op   string
	home string
	name string
}

type fakeLinkClient struct {
	mu    sync.Mutex
	calls []linkCall
}

func (f *fakeLinkClient) record(op, home, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, linkCall{op, home, name})
}

func (f *fakeLinkClient) WriteClusterLink(_ context.Context, home string, link v1alpha1.ClusterLink) error {
	f.record("write", home, link.Name)
	return nil
}

func (f *fakeLinkClient) WriteClusterLinkStatus(_ context.Context, home, name string, _ v1alpha1.ClusterLinkStatus) (bool, error) {
	f.record("status", home, name)
	return false, nil
}

func (f *fakeLinkClient) DeleteClusterLink(_ context.Context, home, name string) error {
	f.record("delete", home, name)
	return nil
}

func (f *fakeLinkClient) deleted() []linkCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []linkCall
	for _, c := range f.calls {
		if c.op == "delete" {
			out = append(out, c)
		}
	}
	return out
}

func linkTestProvider(t *testing.T, f *fakeClusters, client *fakeLinkClient) *Provider {
	t.Helper()
	deps := allClustersDeps(t, f, newRecordingHost())
	deps.Links = &linkRecorder{client: client, registry: f}
	p, err := Build(Config{
		Join:            JoinAllClusters,
		LeasePath:       filepath.Join(t.TempDir(), "ports.json"),
		GrantPath:       DefaultGrantPath,
		MintKubeconfigs: t.TempDir(),
		StateDir:        t.TempDir(),
		StoreEndpoint:   "postgres://postgres@127.0.0.1:25432/k3s?sslmode=disable",
	}, deps, testLogger())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

func TestADeletedClusterLosesBothItsClusterLinks(t *testing.T) {
	gone := v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "gone", Generation: 1, DeletionTimestamp: &metav1.Time{}},
		Spec:       v1alpha1.ClusterSpec{Account: aliceDID, Running: true},
	}
	f := &fakeClusters{items: []v1alpha1.Cluster{gone}}
	client := &fakeLinkClient{}
	linkTestProvider(t, f, client).passAll(context.Background())

	key := clusterKey(t, "gone")
	want := []linkCall{
		{"delete", testHomePrefix + "gone", key + "-apiserver"},
		{"delete", testHomePrefix + "gone", key + "-openbao"},
	}
	if got := client.deleted(); !slices.Equal(got, want) {
		t.Fatalf("deleted %v, want %v: a link of a deleted cluster names a listener that no longer exists", got, want)
	}
}

func TestAClusterSwitchedOffKeepsItsClusterLinks(t *testing.T) {
	off := v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "off", Generation: 1},
		Spec:       v1alpha1.ClusterSpec{Account: aliceDID, Running: false},
	}
	client := &fakeLinkClient{}
	linkTestProvider(t, &fakeClusters{items: []v1alpha1.Cluster{off}}, client).passAll(context.Background())
	if got := client.deleted(); len(got) != 0 {
		t.Fatalf("an OFF cluster's links were deleted: %v; it comes back as the same endpoints", got)
	}
}

func TestTheRecorderWritesIntoTheHomeWorkspaceOfTheClusterKey(t *testing.T) {
	f := &fakeClusters{items: []v1alpha1.Cluster{{ObjectMeta: metav1.ObjectMeta{Name: "one"}}, {ObjectMeta: metav1.ObjectMeta{Name: "two"}}}}
	client := &fakeLinkClient{}
	r := linkRecorder{client: client, registry: f}
	key := clusterKey(t, "two")
	link := v1alpha1.ClusterLink{
		ObjectMeta: metav1.ObjectMeta{Name: key + "-apiserver"},
		Spec:       v1alpha1.ClusterLinkSpec{Cluster: key, Service: v1alpha1.ClusterLinkApiserver, TargetPort: 20010},
	}
	if err := r.WriteLink(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteLinkStatus(context.Background(), key, key+"-apiserver", v1alpha1.ClusterLinkStatus{Phase: v1alpha1.ClusterLinkFailed}); err != nil {
		t.Fatalf("a status write for a link that was never created must not fail: %v", err)
	}
	want := []linkCall{{"write", testHomePrefix + "two", key + "-apiserver"}, {"status", testHomePrefix + "two", key + "-apiserver"}}
	if !slices.Equal(client.calls, want) {
		t.Fatalf("calls = %v, want %v", client.calls, want)
	}
	if err := r.WriteLink(context.Background(), v1alpha1.ClusterLink{Spec: v1alpha1.ClusterLinkSpec{Cluster: "nobody"}}); err == nil {
		t.Fatal("a link for a cluster key no CR has was written somewhere")
	}
}

func TestLinksAreOnOnlyWhenTheDumbpipeBinaryIsNamed(t *testing.T) {
	host := materialiser.NewLocalHost(procgroup.Options{})
	client := &fakeLinkClient{}
	if l := clusterLinks(Config{Join: "web"}, client, &fakeClusters{}, host, testLogger()); l != nil {
		t.Fatalf("links on with no dumbpipe named: %+v", l)
	}
	l := clusterLinks(Config{Join: "web", Dumbpipe: "/usr/local/bin/dumbpipe"}, client, &fakeClusters{}, host, testLogger())
	if l == nil || l.Dumbpipe != "/usr/local/bin/dumbpipe" || l.Recorder == nil || l.Live == nil {
		t.Fatalf("links = %+v", l)
	}
	if l := clusterLinks(Config{Dumbpipe: "/usr/local/bin/dumbpipe"}, client, &fakeClusters{}, host, testLogger()); l != nil {
		t.Fatal("an observe-only provider spawns nothing and must run no link")
	}
}

func linkedStack(t *testing.T) clusterprovider.StackConfig {
	t.Helper()
	return clusterprovider.StackConfig{
		Stack:         clusterprovider.DesignedStack,
		Admin:         func(context.Context, string, string) (kubernetes.Interface, error) { return nil, nil },
		StateDir:      t.TempDir(),
		StoreEndpoint: func(string) (string, error) { return "postgres://postgres@127.0.0.1:25432/k3s?sslmode=disable", nil },
		VaultStoreDSN: func(string) (string, error) {
			return "postgres://postgres:secret@127.0.0.1:21000/openbao?sslmode=disable", nil
		},
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		VaultTLS:         vaultTLSFor(),
		AuditPolicyPath:  filepath.Join(t.TempDir(), "audit-policy.yaml"),
		Links: &clusterprovider.Links{
			Dumbpipe: "/usr/local/bin/dumbpipe",
			Recorder: linkRecorder{client: &fakeLinkClient{}, registry: &fakeClusters{}},
		},
	}
}

func heldLeases(purposes ...portallocator.Purpose) materialiser.Leases {
	l := materialiser.Leases{}
	for i, p := range purposes {
		l[p] = portallocator.Lease{Port: 21000 + 3*i, Width: p.Width(), Cluster: "liveweb", Purpose: p}
	}
	return l
}

var preLinkPurposes = []portallocator.Purpose{
	portallocator.PurposePostgres, portallocator.PurposeOpenBao, portallocator.PurposeKine,
	portallocator.PurposeK3sSecure, portallocator.PurposeK3sControllerManager,
	portallocator.PurposeK3sScheduler, portallocator.PurposeK3sCloudControllerManager,
}

func TestARestartOverAClusterLeasedBeforeLinksExistedDerivesItsArgvWithoutThem(t *testing.T) {
	argvs, err := spawnArgvs(linkedStack(t), "liveweb", heldLeases(preLinkPurposes...))
	if err != nil {
		t.Fatalf("a restart over a lease file written before links existed was refused: %v -- every "+
			"deployed cluster holds such a file, so the first deploy with links would be a crash loop", err)
	}
	for _, argv := range argvs {
		if slices.Contains(argv, "listen-tcp") {
			t.Fatalf("a link with no lease was given an argv: %v", argv)
		}
	}
}

func TestARestartAttributesTheLinksOfAClusterThatHoldsTheirLeases(t *testing.T) {
	all := append(slices.Clone(preLinkPurposes), portallocator.PurposeApiserverLink, portallocator.PurposeOpenBaoLink)
	argvs, err := spawnArgvs(linkedStack(t), "liveweb", heldLeases(all...))
	if err != nil {
		t.Fatal(err)
	}
	links := 0
	for _, argv := range argvs {
		if slices.Contains(argv, "listen-tcp") {
			links++
		}
	}
	if links != 2 {
		t.Fatalf("%d link argvs among %v, want 2: a dumbpipe a previous provider left running is found by its argv", links, argvs)
	}
}

func TestTheDumbpipeBinaryIsReadFromItsEnvironmentVariable(t *testing.T) {
	cfg, err := loadJoin(t, map[string]string{envDumbpipe: " /usr/local/bin/dumbpipe "})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dumbpipe != "/usr/local/bin/dumbpipe" {
		t.Fatalf("Dumbpipe = %q", cfg.Dumbpipe)
	}
	cfg, err = loadJoin(t, nil)
	if err != nil || cfg.Dumbpipe != "" {
		t.Fatalf("an unset %s read as %q (%v)", envDumbpipe, cfg.Dumbpipe, err)
	}
}
