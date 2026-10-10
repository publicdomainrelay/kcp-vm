//go:build linux

package clusterprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

type fakeClusters struct {
	cluster  *v1alpha1.Cluster
	state    v1alpha1.IdentityState
	mount    int
	writes   []v1alpha1.ClusterStatus
	refsErr  error
	readErr  error
	stateErr error

	nodeWrites   []nodeWrite
	nodeWriteErr error

	live     []v1alpha1.Node
	nodesErr error

	unfinalized []Ref
	unfinalErr  error
}

func (f *fakeClusters) Refs(context.Context) ([]Ref, error) {
	if f.refsErr != nil {
		return nil, f.refsErr
	}
	return []Ref{ref()}, nil
}

func (f *fakeClusters) Read(context.Context, Ref) (*v1alpha1.Cluster, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	c := *f.cluster
	return &c, nil
}

func (f *fakeClusters) WriteStatus(_ context.Context, _ Ref, st v1alpha1.ClusterStatus) error {
	f.writes = append(f.writes, st)
	return nil
}

func (f *fakeClusters) MountStatus(context.Context, Ref) int { return f.mount }

func (f *fakeClusters) IdentityState(context.Context, Ref) (v1alpha1.IdentityState, error) {
	return f.state, f.stateErr
}

func (f *fakeClusters) Nodes(_ context.Context, _ Ref) ([]v1alpha1.Node, error) {
	if f.nodesErr != nil {
		return nil, f.nodesErr
	}
	return f.live, nil
}

func (f *fakeClusters) WriteNode(_ context.Context, _ Ref, nodeName string, status v1alpha1.NodeStatus) error {
	f.nodeWrites = append(f.nodeWrites, nodeWrite{node: nodeName, status: status})
	return f.nodeWriteErr
}

func (f *fakeClusters) RemoveFinalizer(_ context.Context, ref Ref) error {
	if f.unfinalErr != nil {
		return f.unfinalErr
	}
	f.unfinalized = append(f.unfinalized, ref)
	return nil
}

type nodeWrite struct {
	node   string
	status v1alpha1.NodeStatus
}

type fakeHost struct {
	mu       sync.Mutex
	live     bool
	spawnErr error
	spawns   [][]procgroup.Step
	reaps    int
}

func (h *fakeHost) Live(string) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.live }

func (h *fakeHost) Spawn(_ context.Context, _ string, steps []procgroup.Step) (*procgroup.Group, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spawns = append(h.spawns, steps)
	if h.spawnErr != nil {
		return nil, h.spawnErr
	}
	h.live = true
	return nil, nil
}

func (h *fakeHost) Reap(context.Context, string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reaps++
	h.live = false
	return nil
}

func (h *fakeHost) Alive(string) []string              { return nil }
func (h *fakeHost) Kill(context.Context, string) error { return nil }

func (h *fakeHost) spawned() [][]procgroup.Step {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.spawns
}

func ref() Ref {
	return Ref{
		Account: "root:4fw93gku",
		DID:     "did:plc:renewallive0000000000000000",
		Home:    "root:4fw93gku:liveweb",
		Name:    "liveweb",
	}
}

func key() string {
	k, err := ref().Key()
	if err != nil {
		panic(err)
	}
	return k
}

func cluster(running bool, phase v1alpha1.ClusterPhase, url string) *v1alpha1.Cluster {
	cl := &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "liveweb", Generation: 1},
		Spec:       v1alpha1.ClusterSpec{Account: "did:plc:renewallive0000000000000000", Running: running},
	}
	cl.Status.Phase = phase
	cl.Status.URL = url
	cl.Status.Instance = "gen-live-1"
	return cl
}

func decider(t *testing.T, host materialiser.Host) *materialiser.Reconciler {
	t.Helper()
	rec, _ := deciderAndPorts(t, host)
	return rec
}

func deciderAndPorts(t *testing.T, host materialiser.Host) (*materialiser.Reconciler, *portallocator.Allocator) {
	t.Helper()
	dir := t.TempDir()
	ports, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(dir, "ports.json"),
		Range:     portallocator.Range{First: 20000, Last: 20099},
		Ephemeral: &portallocator.Range{First: 49152, Last: 65535},
		Probe:     func(int) bool { return false },
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	minted := 0
	rec, err := materialiser.New(materialiser.Options{
		Host:     host,
		Ports:    ports,
		Networks: testNetworks(t),
		Steps: func(materialiser.Observed, materialiser.Leases) ([]procgroup.Step, error) {
			spec := spec()
			return StepsFrom(spec, leases(), ProvenStack)
		},
		Identity:    func(context.Context, materialiser.Observed) error { return nil },
		NewInstance: func() string { minted++; return fmt.Sprintf("gen-%d", minted) },
		Now:         time.Now,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}
	return rec, ports
}

func provider(t *testing.T, fc *fakeClusters, host materialiser.Host, reachable bool, write bool) *Provider {
	t.Helper()
	return providerWithExpiry(t, fc, host, reachable, write, nil)
}

func providerWithExpiry(
	t *testing.T,
	fc *fakeClusters,
	host materialiser.Host,
	reachable bool,
	write bool,
	source NodeExpirySource,
) *Provider {
	t.Helper()
	dh := host
	if dh == nil {
		dh = &fakeHost{}
	}
	p, err := New(Options{
		Registry:         fc,
		Decider:          decider(t, dh),
		Host:             host,
		NodeExpirySource: source,
		WriteStatus:      write,
		Probe:            func(context.Context, string) (int, bool) { return 200, reachable },
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestForeignServingClusterIsLeftAlone(t *testing.T) {
	host := &fakeHost{live: false}
	fc := &fakeClusters{cluster: cluster(true, readyPhase, "https://192.168.5.2:16445")}
	p := provider(t, fc, host, true, true)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !pass.Foreign {
		t.Fatalf("a serving cluster this process does not own was not reported foreign: %+v", pass)
	}
	if got := len(host.spawned()); got != 0 {
		t.Fatalf("a foreign cluster was spawned over (%d spawns)", got)
	}
	if len(fc.writes) != 0 {
		t.Fatalf("a foreign cluster's status was written (%d writes): the write is what moves the live object", len(fc.writes))
	}
	if pass.Result.Phase != readyPhase || pass.Result.URL != "https://192.168.5.2:16445" {
		t.Fatalf("the carried status is not the object's: phase=%q url=%q", pass.Result.Phase, pass.Result.URL)
	}
	if pass.Alert() == "" {
		t.Error("a foreign pass said nothing; a pass that acts differently from what it decided must say why")
	}
}

func TestNoHostIsObserveOnly(t *testing.T) {
	fc := &fakeClusters{cluster: cluster(false, readyPhase, "https://192.168.5.2:16445")}
	p := provider(t, fc, nil, true, true)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !pass.ObserveOnly {
		t.Fatalf("a provider with no Host was not observe-only: %+v", pass)
	}
	if len(fc.writes) != 0 {
		t.Fatalf("an observe-only pass wrote %d statuses", len(fc.writes))
	}
}

func TestRunningClusterWithNoGroupBringsUpAndWritesInitializing(t *testing.T) {
	host := &fakeHost{live: false, spawnErr: errors.New("no k3s binary on this host")}
	fc := &fakeClusters{cluster: cluster(true, "", "")}
	p := provider(t, fc, host, false, true)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pass.Foreign || pass.ObserveOnly {
		t.Fatalf("a spawn candidate was treated as foreign/observe-only: %+v", pass)
	}
	if got := len(host.spawned()); got != 1 {
		t.Fatalf("spawn called %d times, want 1", got)
	}
	steps := host.spawned()[0]
	if len(steps) != 1 || steps[0].Name != procgroup.ComponentK3s {
		t.Fatalf("the spawned group is %v, want the proven stack [k3s-server]", names(steps))
	}
	if pass.Result.Phase != v1alpha1.ClusterInitializing {
		t.Fatalf("phase is %q, want Initializing: a spawn does not prove the apiserver answers", pass.Result.Phase)
	}
	if !pass.Wrote || len(fc.writes) != 1 || fc.writes[0].Phase != v1alpha1.ClusterInitializing {
		t.Fatalf("the Initializing status did not reach kcp: wrote=%v writes=%+v", pass.Wrote, fc.writes)
	}
}

func TestServingClusterThisProcessOwnsIsReady(t *testing.T) {
	host := &fakeHost{live: true}
	fc := &fakeClusters{cluster: cluster(true, v1alpha1.ClusterInitializing, "https://192.168.5.2:16445")}
	fc.cluster.Status.URL = "https://192.168.5.2:16445"
	p := provider(t, fc, host, true, true)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pass.Foreign {
		t.Fatal("a cluster this process owns was reported foreign")
	}
	if pass.Result.Phase != readyPhase {
		t.Fatalf("phase is %q, want Ready (the group is live and the apiserver answered)", pass.Result.Phase)
	}
	if len(fc.writes) != 1 || fc.writes[0].Phase != readyPhase {
		t.Fatalf("Ready did not reach kcp: %+v", fc.writes)
	}
	if got := len(host.spawned()); got != 0 {
		t.Fatalf("a serving cluster was spawned over (%d spawns)", got)
	}
}

func TestAPIServerURLNamesThePortTheApiserverHolds(t *testing.T) {
	fc := &fakeClusters{cluster: cluster(true, "", "")}
	rec, ports := deciderAndPorts(t, &fakeHost{})

	res, err := rec.Reconcile(context.Background(), materialiser.Observed{
		Cluster: *fc.cluster,
		Now:     time.Now(),
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	lease, ok := ports.PortFor("liveweb", portallocator.PurposeK3sSecure)
	if !ok {
		t.Fatal("bringUp wrote a status.URL without taking the apiserver's lease, so nothing " +
			"makes that URL this cluster's address")
	}
	u, err := url.Parse(res.URL)
	if err != nil {
		t.Fatalf("the machine's status.URL is not a URL: %v", err)
	}
	if u.Port() != strconv.Itoa(lease.Port) {
		t.Fatalf("status.URL = %q names port %s and this cluster's apiserver lease is %d. A URL "+
			"naming another port routes a user to ANOTHER CLUSTER'S apiserver, and the symptom is "+
			"an x509 name error rather than a refusal", res.URL, u.Port(), lease.Port)
	}
	for _, p := range materialiser.DefaultPurposes {
		if p == portallocator.PurposeK3sSecure {
			continue
		}
		other, ok := ports.PortFor(key(), p)
		if !ok {
			continue
		}
		if other.Covers(atoi(t, u.Port())) {
			t.Fatalf("status.URL = %q names the %s lease (port %d, width %d) rather than the "+
				"apiserver's. MEASURED: k3s binds *:L and 127.0.0.1:L+1 and NOT --advertise-port, "+
				"and /cacerts answers 200 on L and 401 on P -- so a URL naming P is a URL a node "+
				"loops on with failed-to-get-CA-certs", res.URL, p, other.Port, other.Width)
		}
	}
	if got := APIServerURL(lease.Port); got != res.URL {
		t.Fatalf("APIServerURL gives %q and materialiser writes %q: a join would publish a "+
			"cluster at an address the phase machine does not agree with", got, res.URL)
	}
}

func TestJoinedWritesReadyAtTheURLTheJoinAnsweredAt(t *testing.T) {
	vm := newTestHost()
	if err := vm.Hold(key()); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	fc := &fakeClusters{cluster: cluster(true, "", "")}
	p := provider(t, fc, vm, true, true)

	at := Established{URL: APIServerURL(20000), Instance: "gen-joins-1"}
	pass, err := p.Joined(context.Background(), ref(), at)
	if err != nil {
		t.Fatalf("Joined: %v", err)
	}
	if pass.ObserveOnly || pass.Foreign || pass.Ratchet {
		t.Fatalf("a cluster this process just built was held: foreign=%v observeOnly=%v ratchet=%v",
			pass.Foreign, pass.ObserveOnly, pass.Ratchet)
	}
	if !pass.Wrote || len(fc.writes) != 1 {
		t.Fatalf("the status did not reach kcp: wrote=%v writes=%+v", pass.Wrote, fc.writes)
	}
	got := fc.writes[0]
	if got.Phase != readyPhase {
		t.Fatalf("phase is %q, want Ready: the apiserver answered at %s", got.Phase, at.URL)
	}
	if got.URL != at.URL {
		t.Fatalf("status.URL is %q, want %q -- kcp dials this to resolve the mount", got.URL, at.URL)
	}
	if got.Instance != at.Instance {
		t.Fatalf("status.instance is %q, want %q: an empty one is what makes the mint refuse "+
			"with ErrBindingUnknown", got.Instance, at.Instance)
	}
}

func TestJoinedWithoutAnAnswerWritesInitializing(t *testing.T) {
	vm := newTestHost()
	if err := vm.Hold(key()); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	fc := &fakeClusters{cluster: cluster(true, "", "")}
	p := provider(t, fc, vm, false, true)

	pass, err := p.Joined(context.Background(), ref(), Established{URL: APIServerURL(20000), Instance: "gen-1"})
	if err != nil {
		t.Fatalf("Joined: %v", err)
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want one", fc.writes)
	}
	if fc.writes[0].Phase != v1alpha1.ClusterInitializing {
		t.Fatalf("phase is %q, want Initializing: Initializing is what a caller waiting on this "+
			"cluster reads as 429 + Retry-After", fc.writes[0].Phase)
	}
	if pass.Result.Phase == readyPhase {
		t.Fatal("Ready was claimed over a cluster whose apiserver did not answer")
	}
}

func TestJoinedWithoutAHeldVMDoesNotClaimReady(t *testing.T) {
	fc := &fakeClusters{cluster: cluster(true, "", "")}
	p := provider(t, fc, newTestHost(), true, true)

	pass, err := p.Joined(context.Background(), ref(), Established{URL: APIServerURL(20000), Instance: "gen-1"})
	if err != nil {
		t.Fatalf("Joined: %v", err)
	}
	if pass.Result.Phase == readyPhase {
		t.Fatal("Ready was claimed for a cluster this process holds no VM for")
	}
}

func TestJoinedHoldsTheRatchet(t *testing.T) {
	vm := newTestHost()
	if err := vm.Hold(key()); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	fc := &fakeClusters{cluster: cluster(true, v1alpha1.ClusterDraining, "https://192.168.5.2:16445")}
	p := provider(t, fc, vm, true, true)

	pass, err := p.Joined(context.Background(), ref(), Established{URL: APIServerURL(20000), Instance: "gen-1"})
	if err != nil {
		t.Fatalf("Joined: %v", err)
	}
	if !pass.Ratchet || pass.Result.Phase != v1alpha1.ClusterDraining {
		t.Fatalf("a Draining cluster was moved by the join's pass: ratchet=%v phase=%q", pass.Ratchet, pass.Result.Phase)
	}
	if len(fc.writes) != 0 {
		t.Fatalf("the join's pass wrote over a Draining cluster: %+v", fc.writes)
	}
}

func TestJoinedIsObserveOnlyWithoutAHost(t *testing.T) {
	fc := &fakeClusters{cluster: cluster(false, readyPhase, "https://192.168.5.2:16445")}
	p := provider(t, fc, nil, true, true)

	pass, err := p.Joined(context.Background(), ref(), Established{URL: APIServerURL(20000), Instance: "gen-1"})
	if err != nil {
		t.Fatalf("Joined: %v", err)
	}
	if !pass.ObserveOnly {
		t.Fatalf("a provider with no Host was not observe-only: %+v", pass)
	}
	if len(fc.writes) != 0 {
		t.Fatalf("an observe-only pass wrote %d statuses", len(fc.writes))
	}
	if pass.Result.Phase != readyPhase || pass.Result.URL != "https://192.168.5.2:16445" {
		t.Fatalf("the carried status is not the object's: phase=%q url=%q", pass.Result.Phase, pass.Result.URL)
	}
}

func TestJoinedLeavesAServingClusterItDoesNotOwnAlone(t *testing.T) {
	fc := &fakeClusters{cluster: cluster(true, readyPhase, "https://192.168.5.2:16445")}
	p := provider(t, fc, newTestHost(), true, true)

	pass, err := p.Joined(context.Background(), ref(), Established{URL: APIServerURL(20000), Instance: "gen-1"})
	if err != nil {
		t.Fatalf("Joined: %v", err)
	}
	if !pass.Foreign {
		t.Fatalf("a serving cluster this process does not own was not reported foreign: %+v", pass)
	}
	if len(fc.writes) != 0 {
		t.Fatalf("the join wrote over a cluster it does not own: %+v", fc.writes)
	}
	if pass.Result.URL != "https://192.168.5.2:16445" {
		t.Fatalf("the carried URL is not the object's: %q", pass.Result.URL)
	}
}

func TestJoinedRefusesAURLItCannotPublish(t *testing.T) {
	fc := &fakeClusters{cluster: cluster(true, "", "")}
	p := provider(t, fc, newTestHost(), true, true)

	if _, err := p.Joined(context.Background(), ref(), Established{}); err == nil {
		t.Fatal("a join with no address was accepted")
	}
	if len(fc.writes) != 0 {
		t.Fatalf("a refused pass wrote %+v", fc.writes)
	}
}

func TestWriteStatusOffStillDecides(t *testing.T) {
	host := &fakeHost{live: true}
	fc := &fakeClusters{cluster: cluster(true, readyPhase, "https://192.168.5.2:16445")}
	p := provider(t, fc, host, true, false)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pass.Result.Phase != readyPhase {
		t.Fatalf("phase is %q, want the carried Ready", pass.Result.Phase)
	}
	if len(fc.writes) != 0 {
		t.Fatalf("WriteStatus off wrote %d statuses", len(fc.writes))
	}
}

func TestRevocationPendingRefusesToClaimReady(t *testing.T) {
	host := &fakeHost{live: true}
	fc := &fakeClusters{
		cluster: cluster(true, readyPhase, "https://192.168.5.2:16445"),
		state:   v1alpha1.IdentityRevocationPending,
	}
	p := provider(t, fc, host, true, true)

	_, err := p.Reconcile(context.Background(), ref())
	if err == nil {
		t.Fatal("a RevocationPending identity was reconciled without observing the tenant's ServiceAccount")
	}
	if len(fc.writes) != 0 {
		t.Fatalf("a refused pass wrote %d statuses", len(fc.writes))
	}
}

func TestRunReturnsTheListFailureAndNotThePassFailures(t *testing.T) {
	host := &fakeHost{live: false}
	fc := &fakeClusters{cluster: cluster(true, "", ""), readErr: errors.New("this one is unreadable")}
	p := provider(t, fc, host, false, true)

	ctx, cancel := context.WithCancel(context.Background())
	p.opts.Interval = time.Millisecond
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("a pass failure stopped the loop: %v", err)
	}

	fc.readErr = nil
	fc.refsErr = errors.New("kcp is unreachable")
	if err := p.Run(context.Background()); !errors.Is(err, fc.refsErr) {
		t.Fatalf("a failed LIST was not returned: %v", err)
	}
}

func TestMintConfigFailsClosed(t *testing.T) {
	p := provider(t, &fakeClusters{}, &fakeHost{}, false, false)

	if _, err := p.MintConfig("liveweb"); !errors.Is(err, ErrNoMintConfig) {
		t.Fatalf("an unknown cluster did not fail closed: %v", err)
	}
	if err := p.SetMintConfig("liveweb", nil); err == nil {
		t.Fatal("a nil config was recorded, which turns the fail-closed miss into a nil dereference at the mint")
	}
	if err := p.SetMintConfig("", &rest.Config{Host: "https://x"}); err == nil {
		t.Fatal("a config with no cluster name was recorded")
	}

	cfg := &rest.Config{Host: "https://192.168.5.2:16445", Impersonate: rest.ImpersonationConfig{UserName: "system:serviceaccount:users:gwmint"}}
	if err := p.SetMintConfig("liveweb", cfg); err != nil {
		t.Fatalf("SetMintConfig: %v", err)
	}
	got, err := p.MintConfig("liveweb")
	if err != nil {
		t.Fatalf("MintConfig: %v", err)
	}
	if got.Impersonate.UserName != "system:serviceaccount:users:gwmint" {
		t.Fatalf("the stored credential is not the gwmint impersonation: %+v", got.Impersonate)
	}
	got.Host = "https://someone-elses-cluster"
	again, err := p.MintConfig("liveweb")
	if err != nil {
		t.Fatalf("MintConfig: %v", err)
	}
	if again.Host != cfg.Host {
		t.Fatalf("a consumer mutated the stored credential: %q", again.Host)
	}
}

func TestStatusCarriesWhatTheDecisionDoesNotOwn(t *testing.T) {
	expiry := int64(1790000000)
	cl := cluster(true, readyPhase, "https://192.168.5.2:16445")
	cl.Status.NodeExpiry = &expiry

	st := statusOf(cl, materialiser.Result{Phase: v1alpha1.ClusterUnavailable, URL: cl.Status.URL, Instance: "gen-2"})
	if st.NodeExpiry == nil || *st.NodeExpiry != expiry {
		t.Fatalf("nodeExpiry was dropped by the reconcile: %+v", st.NodeExpiry)
	}
	body, err := statusPatch(st)
	if err != nil {
		t.Fatalf("statusPatch: %v", err)
	}
	if want := `"status":{"phase":"Unavailable","URL":"https://192.168.5.2:16445","instance":"gen-2","nodeExpiry":1790000000}`; !contains(string(body), want) {
		t.Fatalf("status patch is %s, want it to contain %s", body, want)
	}
}

func blockedOps(ops []materialiser.Op) bool {
	for _, op := range ops {
		if op == materialiser.OpBlocked {
			return true
		}
	}
	return false
}

func deleting(cl *v1alpha1.Cluster) *v1alpha1.Cluster {
	at := metav1.NewTime(time.Now())
	cl.DeletionTimestamp = &at
	return cl
}

func answered(cl *v1alpha1.Cluster) *v1alpha1.Cluster {
	cl.Status.Conditions = append(cl.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReachable,
		Status:             metav1.ConditionTrue,
		Reason:             "Probe",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	return cl
}

func TestNoNodeExpirySourceLeavesTheFieldAbsentAndTheGateRefuses(t *testing.T) {
	host := &fakeHost{live: true}
	fc := &fakeClusters{cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, "")))}
	p := provider(t, fc, host, true, true)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !blockedOps(pass.Result.Ops) {
		t.Fatalf("ops = %v, want the block: a provider with no NodeExpirySource originates no value, "+
			"and a gate that cannot compare must not proceed", pass.Result.Ops)
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want one: the refusal still has to reach the object", fc.writes)
	}
	if fc.writes[0].NodeExpiry != nil {
		t.Fatalf("a provider with no NodeExpirySource reported nodeExpiry %d; a guess is "+
			"indistinguishable from a report in the schema, so writing one removes the refusal the "+
			"gate exists to make", *fc.writes[0].NodeExpiry)
	}
}

func TestTheHoldingPassKeepsTheFinalizerAndTheReapingPassReleasesIt(t *testing.T) {
	host := &fakeHost{live: true}
	fc := &fakeClusters{
		cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, ""))),
		live:    []v1alpha1.Node{nodeStatus(v1alpha1.NodeRunning, value(NodeExpiryUnlimited))},
		mount:   401,
	}
	p := providerWithExpiry(t, fc, host, true, true, NodeExpiryRollup)

	held, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if held.Result.Phase != v1alpha1.ClusterDraining {
		t.Fatalf("phase = %q, want %s", held.Result.Phase, v1alpha1.ClusterDraining)
	}
	if !slices.Contains(held.Result.Ops, materialiser.OpShutPath) ||
		!slices.Contains(held.Result.Ops, materialiser.OpAwaitPathShut) {
		t.Fatalf("ops = %v, want the hold: the mounted path answered 401, which is the tenant's "+
			"apiserver refusing through the mount, so the path was REACHED and is still being served",
			held.Result.Ops)
	}
	if len(fc.unfinalized) != 0 {
		t.Fatalf("the finalizer was released while the mounted path answered 401: %v", fc.unfinalized)
	}
	if held.Unfinalized {
		t.Fatal("the pass reported the finalizer released after holding")
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want the Draining status to have been written", fc.writes)
	}

	fc.mount = 503
	reaped, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !slices.Contains(reaped.Result.Ops, materialiser.OpReap) ||
		!slices.Contains(reaped.Result.Ops, materialiser.OpDropFinalizer) {
		t.Fatalf("ops = %v, want the reap and the drop", reaped.Result.Ops)
	}
	if len(fc.unfinalized) != 1 {
		t.Fatalf("the reaping pass released the finalizer %d times, want once: %v",
			len(fc.unfinalized), fc.unfinalized)
	}
	if !reaped.Unfinalized {
		t.Fatal("the reaping pass did not report the finalizer released, so the CR would stay " +
			"terminating for ever and every later pass would reap the same dead cluster")
	}
	if fc.unfinalized[0] != ref() {
		t.Fatalf("the finalizer was released on %+v, want %+v", fc.unfinalized[0], ref())
	}
}

func nodeStatus(phase v1alpha1.NodePhase, expiry *int64) v1alpha1.Node {
	return v1alpha1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "liveweb-node1"},
		Spec:       v1alpha1.NodeSpec{Cluster: "liveweb"},
		Status:     v1alpha1.NodeStatus{Phase: phase, NodeExpiry: expiry},
	}
}

func value(v int64) *int64 { return &v }

func TestANodeWithNoDoorReportsUnlimitedAndTheGateProceeds(t *testing.T) {
	host := &fakeHost{live: true}
	fc := &fakeClusters{
		cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, ""))),
		live:    []v1alpha1.Node{nodeStatus(v1alpha1.NodeRunning, value(NodeExpiryUnlimited))},
	}
	p := providerWithExpiry(t, fc, host, true, true, NodeExpiryRollup)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if blockedOps(pass.Result.Ops) {
		t.Fatalf("ops = %v for %q: a substrate with no door has no session cap at all, so its "+
			"nodes report UNLIMITED and there is no deadline for the gate to outlive",
			pass.Result.Ops, pass.Result.Alert)
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want one", fc.writes)
	}
	got := fc.writes[0].NodeExpiry
	if got == nil || *got != NodeExpiryUnlimited {
		t.Fatalf("nodeExpiry = %v, want an explicit 0. ABSENT is what keeps the gate refusing, so "+
			"a doorless substrate whose nodes report nothing is a cluster nobody can delete; 0 is a FACT "+
			"about this substrate, not a guess about a door it does not have", got)
	}
}

func TestTheGateRefusesWhenItWouldOutliveTheReportedDeadline(t *testing.T) {
	host := &fakeHost{live: true}
	deadline := time.Now().Add(5 * time.Second).Unix()
	fc := &fakeClusters{
		cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, ""))),
		live:    []v1alpha1.Node{nodeStatus(v1alpha1.NodeRunning, &deadline)},
	}
	p := providerWithExpiry(t, fc, host, true, true, NodeExpiryRollup)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !blockedOps(pass.Result.Ops) {
		t.Fatalf("ops = %v, want the block: the gate outlives nodeExpiry %d by construction, and a "+
			"teardown that outlives the session cap is a lost write", pass.Result.Ops, deadline)
	}
	if !contains(pass.Result.Alert, "would outlive") {
		t.Fatalf("alert = %q, want the refusal that names the deadline the gate would outlive, not "+
			"the one for a gate that cannot compare at all", pass.Result.Alert)
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want one", fc.writes)
	}
	if got := fc.writes[0].NodeExpiry; got == nil || *got != deadline {
		t.Fatalf("the rolled-up nodeExpiry is %v, want the live node's own report %d", got, deadline)
	}
}

func TestADeadlineTheClusterCarriesIsGatedOnWhenThisProviderOriginatesNone(t *testing.T) {
	host := &fakeHost{live: true}
	deadline := time.Now().Add(5 * time.Second).Unix()
	cl := deleting(answered(cluster(false, v1alpha1.ClusterInitializing, "")))
	cl.Status.NodeExpiry = &deadline
	fc := &fakeClusters{cluster: cl}
	p := providerWithExpiry(t, fc, host, true, true, nil)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !blockedOps(pass.Result.Ops) {
		t.Fatalf("ops = %v, want the block: the gate outlives the carried nodeExpiry %d by "+
			"construction, and a teardown that outlives the session cap is a lost write",
			pass.Result.Ops, deadline)
	}
	if !contains(pass.Result.Alert, "would outlive") {
		t.Fatalf("alert = %q, want the refusal that names the deadline the gate would outlive",
			pass.Result.Alert)
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want one", fc.writes)
	}
	if got := fc.writes[0].NodeExpiry; got == nil || *got != deadline {
		t.Fatalf("the carried nodeExpiry is %v, want the deadline %d untouched: a provider that "+
			"originates no value is not the writer of this one, and dropping it would hand the gate "+
			"a deadline it cannot compare against", got, deadline)
	}
}

func TestTheDerivationIsRecomputedEveryPassAndNeverRevisesANodesOwnReport(t *testing.T) {
	deadline := int64(4102444800)
	host := &fakeHost{live: true}
	fc := &fakeClusters{
		cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, ""))),
		live:    []v1alpha1.Node{nodeStatus(v1alpha1.NodeRunning, &deadline)},
	}
	consulted := 0
	source := func(cl *v1alpha1.Cluster, nodes []v1alpha1.Node) (int64, bool) {
		consulted++
		return NodeExpiryRollup(cl, nodes)
	}
	p := providerWithExpiry(t, fc, host, true, true, source)

	const passes = 3
	for i := 0; i < passes; i++ {
		if _, err := p.Reconcile(context.Background(), ref()); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}
	if consulted != passes {
		t.Fatalf("the roll-up was derived %d times over %d passes: the Cluster value is a "+
			"DERIVATION from the live nodes and not a value anyone reports, and freezing it at the "+
			"moment before the first node exists is what leaves a cluster deletable while its nodes "+
			"live", consulted, passes)
	}
	if len(fc.writes) != passes {
		t.Fatalf("writes = %d, want %d: every pass re-derives and re-writes", len(fc.writes), passes)
	}
	for i, st := range fc.writes {
		if st.NodeExpiry == nil || *st.NodeExpiry != deadline {
			t.Fatalf("write %d carries nodeExpiry %v, want the live node's report %d", i, st.NodeExpiry, deadline)
		}
	}
	if len(fc.nodeWrites) != 0 {
		t.Fatalf("the roll-up wrote %d node statuses: a node's report is a FACT and the Cluster value "+
			"is derived FROM it, so a provider that writes a node's status is exactly how a report gets "+
			"revised past the gate it feeds", len(fc.nodeWrites))
	}
}

func TestARollupThatGoesAbsentTakesTheClusterValueWithIt(t *testing.T) {
	deadline := int64(4102444800)
	host := &fakeHost{live: true}
	fc := &fakeClusters{
		cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, ""))),
		live:    []v1alpha1.Node{nodeStatus(v1alpha1.NodeRunning, &deadline)},
	}
	p := providerWithExpiry(t, fc, host, true, true, NodeExpiryRollup)

	if _, err := p.Reconcile(context.Background(), ref()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := fc.writes[0].NodeExpiry; got == nil || *got != deadline {
		t.Fatalf("first pass carried nodeExpiry %v, want %d", got, deadline)
	}

	fc.live[0].Status.NodeExpiry = nil
	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !blockedOps(pass.Result.Ops) {
		t.Fatalf("ops = %v, want the block: one live node with no report makes the roll-up ABSENT, "+
			"and a gate that cannot compare must not proceed", pass.Result.Ops)
	}
	if got := fc.writes[1].NodeExpiry; got != nil {
		t.Fatalf("the Cluster still carries nodeExpiry %d over a live node that reported nothing: "+
			"0 there means UNLIMITED, which is what makes the cluster deletable while the node lives", *got)
	}
	body, err := statusPatch(fc.writes[1])
	if err != nil {
		t.Fatalf("statusPatch: %v", err)
	}
	if !contains(string(body), `"nodeExpiry":null`) {
		t.Fatalf("the patch is %s, want an EXPLICIT null: a merge patch that OMITS nodeExpiry leaves "+
			"the previous value on the object, so a roll-up that went absent would leave a stale 0 "+
			"behind and the next gate would proceed over it", body)
	}
}

func TestTheRollupIsTheMinimumOverTheLiveNodes(t *testing.T) {
	node := nodeStatus
	cases := []struct {
		name     string
		nodes    []v1alpha1.Node
		expiry   int64
		reported bool
	}{
		{
			name:     "the empty set is 0, so a cluster with no nodes is deletable",
			nodes:    nil,
			expiry:   NodeExpiryUnlimited,
			reported: true,
		},
		{
			name:     "0 counts as infinity, so one unlimited node does not cap the cluster",
			nodes:    []v1alpha1.Node{node(v1alpha1.NodeRunning, value(NodeExpiryUnlimited)), node(v1alpha1.NodeRunning, value(500))},
			expiry:   500,
			reported: true,
		},
		{
			name:     "the minimum over the live nodes, whatever their phase",
			nodes:    []v1alpha1.Node{node(v1alpha1.NodeRunning, value(900)), node(v1alpha1.NodePending, value(300))},
			expiry:   300,
			reported: true,
		},
		{
			name:     "every live node unlimited is unlimited",
			nodes:    []v1alpha1.Node{node(v1alpha1.NodeRunning, value(NodeExpiryUnlimited)), node(v1alpha1.NodeRunning, value(NodeExpiryUnlimited))},
			expiry:   NodeExpiryUnlimited,
			reported: true,
		},
		{
			name:     "one live node absent makes the whole roll-up ABSENT, not 0",
			nodes:    []v1alpha1.Node{node(v1alpha1.NodeRunning, value(300)), node(v1alpha1.NodeRunning, nil)},
			reported: false,
		},
		{
			name:     "a draining node carries no roll-up",
			nodes:    []v1alpha1.Node{node(v1alpha1.NodeDraining, value(300))},
			expiry:   NodeExpiryUnlimited,
			reported: true,
		},
		{
			name:     "a draining node with no report does not make the roll-up absent",
			nodes:    []v1alpha1.Node{node(v1alpha1.NodeDraining, nil), node(v1alpha1.NodeRunning, value(700))},
			expiry:   700,
			reported: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expiry, reported := NodeExpiryRollup(cluster(true, v1alpha1.ClusterInitializing, ""), tc.nodes)
			if reported != tc.reported {
				t.Fatalf("reported = %v, want %v: reported=false is the refusal, and collapsing "+
					"it to a value is how a cluster with live nodes becomes deletable", reported, tc.reported)
			}
			if !reported {
				return
			}
			if expiry != tc.expiry {
				t.Fatalf("expiry = %d, want %d", expiry, tc.expiry)
			}
		})
	}
}

func TestTheClusterCarriesTheRollupOverItsNodes(t *testing.T) {
	deadline := int64(4102444800)
	host := &fakeHost{live: true}
	fc := &fakeClusters{
		cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, ""))),
		live: []v1alpha1.Node{{
			ObjectMeta: metav1.ObjectMeta{Name: "liveweb-node1"},
			Spec:       v1alpha1.NodeSpec{Cluster: "liveweb"},
			Status:     v1alpha1.NodeStatus{Phase: v1alpha1.NodeRunning, NodeExpiry: &deadline},
		}},
	}
	p := providerWithExpiry(t, fc, host, true, true, NodeExpiryRollup)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if blockedOps(pass.Result.Ops) {
		t.Fatalf("ops = %v: the gate refused over a deadline that is a century out", pass.Result.Ops)
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want one", fc.writes)
	}
	if got := fc.writes[0].NodeExpiry; got == nil || *got != deadline {
		t.Fatalf("the Cluster carries nodeExpiry %v, want the roll-up over its live nodes (%d). "+
			"The cluster value is DERIVED from the node CRs: a cluster whose roll-up is wrongly 0 is "+
			"deletable while live nodes exist, which is the failure this whole mechanism prevents", got, deadline)
	}
}

func TestAClusterWhoseLiveNodeReportsNothingIsUndeletable(t *testing.T) {
	host := &fakeHost{live: true}
	fc := &fakeClusters{
		cluster: deleting(answered(cluster(false, v1alpha1.ClusterInitializing, ""))),
		live: []v1alpha1.Node{{
			ObjectMeta: metav1.ObjectMeta{Name: "liveweb-node1"},
			Spec:       v1alpha1.NodeSpec{Cluster: "liveweb"},
			Status:     v1alpha1.NodeStatus{Phase: v1alpha1.NodeRunning},
		}},
	}
	p := providerWithExpiry(t, fc, host, true, true, NodeExpiryRollup)

	pass, err := p.Reconcile(context.Background(), ref())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !blockedOps(pass.Result.Ops) {
		t.Fatalf("ops = %v, want the block: a live node with no report rolls up to ABSENT, and a "+
			"gate that cannot compare must not proceed", pass.Result.Ops)
	}
	if !contains(pass.Result.Alert, "cannot compare") {
		t.Fatalf("alert = %q, want the refusal for a gate that cannot compare, not a guess", pass.Result.Alert)
	}
	if len(fc.writes) != 1 {
		t.Fatalf("writes = %+v, want one: the refusal still has to reach the object", fc.writes)
	}
	if fc.writes[0].NodeExpiry != nil {
		t.Fatalf("a cluster whose live node reported nothing carried nodeExpiry %d; 0 there means "+
			"UNLIMITED and makes the cluster deletable while the node exists", *fc.writes[0].NodeExpiry)
	}
}

func TestNewRefusesWhatItCannotBeSafeWith(t *testing.T) {
	if _, err := New(Options{Decider: decider(t, &fakeHost{})}); err == nil {
		t.Error("a provider with no Registry was built")
	}
	if _, err := New(Options{Registry: &fakeClusters{}}); !errors.Is(err, ErrNoDecider) {
		t.Error("a provider with no phase machine was built; the default would be a second one living in this package")
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("port %q is not a number: %v", s, err)
	}
	return n
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestDrainingIsAOneWayRatchet(t *testing.T) {
	for _, running := range []bool{true, false} {
		for _, reachable := range []bool{true, false} {
			for _, live := range []bool{true, false} {
				for _, url := range []string{"", "https://192.168.5.2:16445"} {
					name := fmt.Sprintf("running=%v/reachable=%v/live=%v/url=%q", running, reachable, live, url)
					t.Run(name, func(t *testing.T) {
						host := &fakeHost{live: live}
						fc := &fakeClusters{cluster: cluster(running, v1alpha1.ClusterDraining, url)}
						p := provider(t, fc, host, reachable, true)

						pass, err := p.Reconcile(context.Background(), ref())
						if err != nil {
							t.Fatalf("Reconcile: %v", err)
						}
						if !pass.Ratchet {
							t.Fatalf("a Draining cluster was not held: foreign=%v observeOnly=%v phase=%q",
								pass.Foreign, pass.ObserveOnly, pass.Result.Phase)
						}
						if pass.Result.Phase != v1alpha1.ClusterDraining {
							t.Fatalf("phase moved OUT of Draining to %q: the ratchet is cancellable", pass.Result.Phase)
						}
						if n := len(host.spawned()); n != 0 {
							t.Fatalf("a Draining cluster was spawned over (%d spawns)", n)
						}
						if len(fc.writes) != 0 {
							t.Fatalf("a Draining cluster's status was written: %+v", fc.writes)
						}
						if pass.Alert() == "" {
							t.Error("the hold said nothing")
						}
					})
				}
			}
		}
	}

	decided := 0
	for _, running := range []bool{true, false} {
		for _, reachable := range []bool{true, false} {
			host := &fakeHost{live: reachable}
			fc := &fakeClusters{cluster: cluster(running, v1alpha1.ClusterInitializing, "https://192.168.5.2:16445")}
			p := provider(t, fc, host, reachable, true)

			pass, err := p.Reconcile(context.Background(), ref())
			if err != nil {
				t.Fatalf("control Reconcile: %v", err)
			}
			if pass.Ratchet {
				t.Fatalf("control running=%v reachable=%v was held as if Draining", running, reachable)
			}
			if len(host.spawned()) != 0 || len(fc.writes) != 0 {
				decided++
			}
		}
	}
	if decided == 0 {
		t.Fatal("no control pass acted on anything: the ratchet test would pass for a provider " +
			"that refuses every cluster, which proves nothing about Draining")
	}
}

type testHost struct {
	held map[string]bool
}

func newTestHost() *testHost { return &testHost{held: map[string]bool{}} }

func (h *testHost) Hold(cluster string) error {
	if h.held[cluster] {
		return fmt.Errorf("testHost: %s is already held", cluster)
	}
	h.held[cluster] = true
	return nil
}

func (h *testHost) Live(cluster string) bool { return h.held[cluster] }

func (h *testHost) Spawn(context.Context, string, []procgroup.Step) (*procgroup.Group, error) {
	return nil, errors.New("testHost: Spawn is not what these tests exercise")
}

func (h *testHost) Reap(context.Context, string) error { return nil }
func (h *testHost) Alive(string) []string              { return nil }
func (h *testHost) Kill(context.Context, string) error { return nil }

var _ materialiser.Host = (*testHost)(nil)
