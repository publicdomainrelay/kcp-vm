//go:build linux

package clusterprovider

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterlink"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const (
	apiserverSeed     = "f8555129991792f402f21f6a01b0c027744b0b02c4b5d1f0e1bf979f722991d8"
	apiserverEndpoint = "cb6686656591d6e18858e0262a30c92700543f3230d3d483a4976cbfcbfc693c"
	apiserverTicket   = "endpointadfwnbtfmwi5nymildqcmkrqzetqavb7giynhveduslwzp6l7rutybiaenuhi5dqom5c6l3von3tcljrfzzgk3dbpexg4mbonfzg62bonruw42zof4aqayltuyk3xcqdaeagiwullc5yuaybacwbcaabxofagaiaycuaaff3ribq"
	openbaoSeed       = "30b0a6d2e8c91c167a71bfa88b1f05a1775a84144ce0f3a3f3a65650ef5a67c4"
	openbaoTicket     = "endpointabqxdprknrzh52p6hssdgbbu2ost4ax2zqh3fljn5o7plucisqcukaa"
	apiserverStable   = "endpointadfwnbtfmwi5nymildqcmkrqzetqavb7giynhveduslwzp6l7rutyaa"
)

type linkKV struct {
	mu   sync.Mutex
	data map[string]map[string]any
}

func (s *linkKV) Read(_ context.Context, path string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[path]
	return d, ok, nil
}

func (s *linkKV) Create(_ context.Context, path string, data map[string]any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[path]; ok {
		return false, nil
	}
	s.data[path] = data
	return true, nil
}

type linkWrite struct {
	name   string
	full   bool
	link   v1alpha1.ClusterLink
	status v1alpha1.ClusterLinkStatus
}

type recordedLinks struct {
	mu     sync.Mutex
	writes []linkWrite
}

func (r *recordedLinks) WriteLink(_ context.Context, link v1alpha1.ClusterLink) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, linkWrite{name: link.Name, full: true, link: link, status: link.Status})
	return nil
}

func (r *recordedLinks) WriteLinkStatus(_ context.Context, cluster, name string, status v1alpha1.ClusterLinkStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, linkWrite{name: name, status: status})
	return nil
}

func (r *recordedLinks) last(name string) (linkWrite, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.writes) - 1; i >= 0; i-- {
		if r.writes[i].name == name {
			return r.writes[i], true
		}
	}
	return linkWrite{}, false
}

func (r *recordedLinks) await(t *testing.T, name string, phase v1alpha1.ClusterLinkPhase) linkWrite {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if w, ok := r.last(name); ok && w.status.Phase == phase {
			return w
		}
		time.Sleep(20 * time.Millisecond)
	}
	w, _ := r.last(name)
	t.Fatalf("%s never reached %s; last write %+v", name, phase, w)
	return linkWrite{}
}

func fakeDumbpipe(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dumbpipe")
	script := "#!/bin/sh\n" +
		"case \"$IROH_SECRET\" in\n" +
		apiserverSeed + ") t=" + apiserverTicket + " ;;\n" +
		openbaoSeed + ") t=" + openbaoTicket + " ;;\n" +
		"*) echo \"no key this fake knows: $IROH_SECRET\" >&2; exit 3 ;;\n" +
		"esac\n" +
		"echo \"Forwarding incoming requests to '$3'.\"\n" +
		"echo 'To connect, use e.g.:'\n" +
		"echo \"dumbpipe connect-tcp $t\"\n" +
		"exec sleep 3600\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func seeds(t *testing.T) *bytes.Reader {
	t.Helper()
	a, _ := hex.DecodeString(apiserverSeed)
	b, _ := hex.DecodeString(openbaoSeed)
	return bytes.NewReader(append(a, b...))
}

func linkLeases(apiserver int) materialiser.Leases {
	l := designedLeases(apiserver)
	l[portallocator.PurposeApiserverLink] = portallocator.Lease{Port: 21012, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeApiserverLink}
	l[portallocator.PurposeOpenBaoLink] = portallocator.Lease{Port: 21013, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeOpenBaoLink}
	return l
}

type linkHarness struct {
	kv       *linkKV
	recorder *recordedLinks
	live     bool
	mu       sync.Mutex
	links    *Links
}

func newLinkHarness(t *testing.T) *linkHarness {
	h := &linkHarness{kv: &linkKV{data: map[string]map[string]any{}}, recorder: &recordedLinks{}}
	h.links = &Links{
		Dumbpipe: fakeDumbpipe(t),
		Recorder: h.recorder,
		Live: func(string) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.live
		},
		Keys:   func(context.Context, string, int) (clusterlink.KeyStore, error) { return h.kv, nil },
		Random: seeds(t),
		Retry:  10 * time.Millisecond,
		Log:    discard(),
	}
	return h
}

func (h *linkHarness) setLive(v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.live = v
}

func linkStackConfig(t *testing.T, links *Links) StackConfig {
	t.Helper()
	return StackConfig{
		Stack:            DesignedStack,
		Admin:            fakeAdmin(),
		StateDir:         t.TempDir(),
		StoreEndpoint:    func(string) (string, error) { return "postgres://postgres@127.0.0.1:21001/k3s?sslmode=disable", nil },
		VaultStoreDSN:    func(string) (string, error) { return fakeVaultStoreDSN(), nil },
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		VaultTLS:         testVaultTLS(),
		AuditPolicyPath:  "/tmp/audit-policy.yaml",
		Links:            links,
	}
}

func linkSteps(t *testing.T, steps []procgroup.Step) []procgroup.Step {
	t.Helper()
	var out []procgroup.Step
	for _, s := range steps {
		if s.Name == procgroup.ComponentApiserverLink || s.Name == procgroup.ComponentOpenBaoLink {
			out = append(out, s)
		}
	}
	if len(out) != 2 {
		t.Fatalf("want two link steps, got %v", names(steps))
	}
	return out
}

func TestTheLinksAreComponentsOfTheGroupStartedAfterK3sInFrontOfTheApiserverAndTheVault(t *testing.T) {
	cfg := linkStackConfig(t, newLinkHarness(t).links)
	steps, err := StepsFor(cfg)(observed(), linkLeases(21003))
	if err != nil {
		t.Fatal(err)
	}
	got := names(steps)
	k3s := -1
	for i, n := range got {
		if n == string(procgroup.ComponentK3s) {
			k3s = i
		}
	}
	if k3s < 0 || len(got) < k3s+3 || got[len(got)-2] != string(procgroup.ComponentApiserverLink) ||
		got[len(got)-1] != string(procgroup.ComponentOpenBaoLink) {
		t.Fatalf("steps = %v, want the two links last, after k3s", got)
	}
	links := linkSteps(t, steps)
	want := [][]string{
		{cfg.Links.Dumbpipe, "listen-tcp", "--host", "127.0.0.1:21003", "--ipv4-addr", "0.0.0.0:21012"},
		{cfg.Links.Dumbpipe, "listen-tcp", "--host", "127.0.0.1:21004", "--ipv4-addr", "0.0.0.0:21013"},
	}
	for i, s := range links {
		if !reflect.DeepEqual(s.Spec.Argv, want[i]) {
			t.Fatalf("%s argv = %v, want %v", s.Name, s.Spec.Argv, want[i])
		}
		if s.Prepare == nil || s.Ready == nil {
			t.Fatalf("%s has no prepare or no gate", s.Name)
		}
		for _, kv := range s.Spec.Env {
			if strings.HasPrefix(kv, clusterlink.EnvSecret+"=") {
				t.Fatalf("%s carries its secret before the vault it lives in is up", s.Name)
			}
		}
	}
}

func TestLinksWithoutAVaultToKeepTheirKeysAreRefused(t *testing.T) {
	cfg := linkStackConfig(t, newLinkHarness(t).links)
	cfg.Stack = ProvenStack
	cfg.VaultTLS = nil
	if _, err := StepsFor(cfg)(observed(), linkLeases(21003)); !errors.Is(err, ErrNoLinkSeam) {
		t.Fatalf("StepsFor = %v, want ErrNoLinkSeam", err)
	}
}

func TestLinksWithNoLeaseAreRefusedAndAStartupScanDerivesTheRestWithoutThem(t *testing.T) {
	cfg := linkStackConfig(t, newLinkHarness(t).links)
	held := designedLeases(21003)
	if _, err := StepsFor(cfg)(observed(), held); !errors.Is(err, ErrNoLease) {
		t.Fatalf("StepsFor without link leases = %v, want ErrNoLease", err)
	}
	steps, err := StepsFor(cfg.WithoutUnleasedLinks(held))(observed(), held)
	if err != nil {
		t.Fatalf("a cluster leased before links existed must still restart: %v", err)
	}
	for _, s := range steps {
		if s.Name == procgroup.ComponentApiserverLink || s.Name == procgroup.ComponentOpenBaoLink {
			t.Fatalf("a link with no lease was derived: %v", names(steps))
		}
	}
	if cfg.WithoutUnleasedLinks(linkLeases(21003)).Links == nil {
		t.Fatal("a cluster holding both link leases lost its links")
	}
}

func spawnLinks(t *testing.T, h *linkHarness, cfg StackConfig) *procgroup.Group {
	t.Helper()
	steps, err := StepsFor(cfg)(observed(), linkLeases(21003))
	if err != nil {
		t.Fatal(err)
	}
	g, err := procgroup.Spawn(context.Background(), linkSteps(t, steps), procgroup.Options{GracePeriod: 2 * time.Second})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	h.setLive(true)
	return g
}

func TestEachLinkIsRecordedListeningWithTheStableTicketAndTheDidKeyOfItsKey(t *testing.T) {
	h := newLinkHarness(t)
	g := spawnLinks(t, h, linkStackConfig(t, h.links))
	defer g.Reap(context.Background())

	api := h.recorder.await(t, "liveweb-apiserver", v1alpha1.ClusterLinkListening)
	if !api.full || api.link.Spec != (v1alpha1.ClusterLinkSpec{Cluster: "liveweb", Service: v1alpha1.ClusterLinkApiserver, TargetPort: 21003}) {
		t.Fatalf("apiserver link written as %+v", api)
	}
	if api.status.EndpointID != apiserverEndpoint || api.status.Ticket != apiserverStable ||
		!strings.HasPrefix(api.status.Did, "did:key:z6Mk") || api.status.Message != "" {
		t.Fatalf("apiserver link status = %+v", api.status)
	}
	bao := h.recorder.await(t, "liveweb-openbao", v1alpha1.ClusterLinkListening)
	if bao.link.Spec.TargetPort != 21004 || bao.status.Ticket != openbaoTicket {
		t.Fatalf("openbao link = %+v", bao)
	}
	stored := h.kv.data[string(v1alpha1.ClusterLinkApiserver)]
	if stored[clusterlink.FieldSecret] != apiserverSeed || stored[clusterlink.FieldEndpointID] != apiserverEndpoint ||
		stored[clusterlink.FieldDid] != api.status.Did {
		t.Fatalf("the vault holds %v", stored)
	}
}

func TestAProviderRestartBringsTheLinksBackAsTheSameEndpoints(t *testing.T) {
	h := newLinkHarness(t)
	cfg := linkStackConfig(t, h.links)
	g := spawnLinks(t, h, cfg)
	before := h.recorder.await(t, "liveweb-apiserver", v1alpha1.ClusterLinkListening).status

	h.setLive(false)
	if err := g.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopped := h.recorder.await(t, "liveweb-apiserver", v1alpha1.ClusterLinkPending)
	if stopped.full || stopped.status.EndpointID != apiserverEndpoint {
		t.Fatalf("a reaped link was written as %+v; want a status-only Pending", stopped)
	}

	h.links.Random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 64))
	g = spawnLinks(t, h, cfg)
	defer g.Reap(context.Background())
	after := h.recorder.await(t, "liveweb-apiserver", v1alpha1.ClusterLinkListening).status
	if after.EndpointID != before.EndpointID || after.Did != before.Did || after.Ticket != before.Ticket {
		t.Fatalf("after the restart the link is %s %s, before it was %s %s: a node secret holds the ticket "+
			"written once", after.EndpointID, after.Ticket, before.EndpointID, before.Ticket)
	}
	id, err := clusterlink.ParseTicket(after.Ticket)
	if err != nil || id != before.EndpointID {
		t.Fatalf("the restarted ticket names %s (%v), want %s", id, err, before.EndpointID)
	}
}

func TestALinkThatDiesIsRecordedFailedWithWhatItLastSaid(t *testing.T) {
	h := newLinkHarness(t)
	g := spawnLinks(t, h, linkStackConfig(t, h.links))
	defer g.Reap(context.Background())
	h.recorder.await(t, "liveweb-openbao", v1alpha1.ClusterLinkListening)

	p, ok := g.Process(procgroup.ComponentOpenBaoLink)
	if !ok {
		t.Fatalf("the openbao link is not in the group: %v", g.Order())
	}
	if err := p.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	failed := h.recorder.await(t, "liveweb-openbao", v1alpha1.ClusterLinkFailed)
	if failed.full || !strings.Contains(failed.status.Message, "dumbpipe exited") ||
		!strings.Contains(failed.status.Message, openbaoTicket) {
		t.Fatalf("Failed status = %+v", failed.status)
	}
	if w, _ := h.recorder.last("liveweb-apiserver"); w.status.Phase != v1alpha1.ClusterLinkListening {
		t.Fatalf("the apiserver link was touched by the openbao link's death: %+v", w)
	}
}

func TestTheLinksAreReapedWithTheGroupFirstInReverseOrder(t *testing.T) {
	h := newLinkHarness(t)
	g := spawnLinks(t, h, linkStackConfig(t, h.links))
	if got := g.Order(); len(got) != 2 || got[0] != procgroup.ComponentApiserverLink || got[1] != procgroup.ComponentOpenBaoLink {
		t.Fatalf("group order = %v", got)
	}
	var pids []int
	for _, c := range g.Order() {
		p, _ := g.Process(c)
		pids = append(pids, p.PID)
	}
	h.setLive(false)
	if err := g.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err == nil {
			t.Fatalf("pid %d is still alive after the reap", pid)
		}
	}
}

func TestALinkWhoseKeyTheFakeDoesNotKnowNeverPassesItsGate(t *testing.T) {
	h := newLinkHarness(t)
	h.links.Random = bytes.NewReader(bytes.Repeat([]byte{0x11}, 64))
	steps, err := StepsFor(linkStackConfig(t, h.links))(observed(), linkLeases(21003))
	if err != nil {
		t.Fatal(err)
	}
	_, err = procgroup.Spawn(context.Background(), linkSteps(t, steps), procgroup.Options{})
	if err == nil || !strings.Contains(err.Error(), "exited before it printed a ticket") {
		t.Fatalf("Spawn = %v", err)
	}
}
