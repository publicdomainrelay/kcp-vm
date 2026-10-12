package kcpserver

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/apiserverfront"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

type journal struct {
	mu     sync.Mutex
	events []string
}

func (j *journal) add(s string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, s)
}

func (j *journal) index(s string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Index(j.events, s)
}

type fakeManager struct {
	hostsvc.Manager
	j       *journal
	mu      sync.Mutex
	running map[string]int
	pid     int
	onStart func(spec hostsvc.UnitSpec) error
}

func (m *fakeManager) Start(ctx context.Context, spec hostsvc.UnitSpec) (hostsvc.UnitStatus, error) {
	if m.onStart != nil {
		if err := m.onStart(spec); err != nil {
			return hostsvc.UnitStatus{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pid++
	m.running[spec.Name] = m.pid
	m.j.add("start " + spec.Name)
	return hostsvc.UnitStatus{Name: spec.Name, State: hostsvc.Active, MainPID: m.pid, InvocationID: "inv-" + spec.Name}, nil
}

func (m *fakeManager) Stop(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.running[name]; ok {
		m.j.add("stop " + name)
	}
	delete(m.running, name)
	return nil
}

func (m *fakeManager) ResetFailed(ctx context.Context, name string) error { return nil }

func (m *fakeManager) Inspect(ctx context.Context, name string) (hostsvc.UnitStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pid, ok := m.running[name]; ok {
		return hostsvc.UnitStatus{Name: name, State: hostsvc.Active, MainPID: pid}, nil
	}
	return hostsvc.UnitStatus{Name: name, State: hostsvc.Inactive}, nil
}

func (m *fakeManager) isRunning(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[name]
	return ok
}

type fakeFront struct {
	j       *journal
	slots   string
	backend string
	onSet   func(backend string) error
}

func (f *fakeFront) Set(ctx context.Context, backend string) (apiserverfront.Status, error) {
	if f.onSet != nil {
		if err := f.onSet(backend); err != nil {
			return apiserverfront.Status{}, err
		}
	}
	f.backend = backend
	f.j.add("flip " + backend)
	return apiserverfront.Status{Backend: backend}, nil
}

func (f *fakeFront) Status(ctx context.Context) (apiserverfront.Status, error) {
	return apiserverfront.Status{Backend: f.backend, Open: map[string]int{}}, nil
}

func (f *fakeFront) Drain(ctx context.Context, backend string) (int, error) { return 0, nil }

type fakeAdmin struct {
	j         *journal
	mu        sync.Mutex
	readyPort map[int]bool
	asked     []int
	holder    string
	onDelete  func() error
}

func (a *fakeAdmin) Readyz(ctx context.Context, port int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, port)
	if a.readyPort[port] {
		return 200, nil
	}
	return 503, nil
}

func (a *fakeAdmin) Lease(ctx context.Context) (Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Lease{Found: a.holder != "", Holder: a.holder, RenewTime: time.Now()}, nil
}

func (a *fakeAdmin) DeleteLease(ctx context.Context) error {
	if a.onDelete != nil {
		if err := a.onDelete(); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.holder = "new_holder"
	a.mu.Unlock()
	a.j.add("delete lease")
	return nil
}

func (a *fakeAdmin) ProbeWorkspace(ctx context.Context, name string) error {
	a.j.add("probe")
	return nil
}

type rig struct {
	dir    string
	shared string
	slots  string
	j      *journal
	m      *fakeManager
	f      *fakeFront
	a      *fakeAdmin
	ports  map[int]int
	cfg    RollConfig
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{dir: dir, shared: filepath.Join(dir, "shared"), slots: filepath.Join(dir, "kcp-slots.json"), j: &journal{},
		ports: map[int]int{}}
	os.MkdirAll(r.shared, 0o700)
	for _, f := range SharedFiles {
		os.WriteFile(filepath.Join(r.shared, f), []byte("material "+f), 0o600)
	}
	r.m = &fakeManager{j: r.j, running: map[string]int{"socialweb-kcp-t-slot-a.service": 900}, pid: 1000}
	r.f = &fakeFront{j: r.j, slots: r.slots, backend: "127.0.0.1:40000"}
	r.a = &fakeAdmin{j: r.j, readyPort: map[int]bool{41001: true}, holder: "old_holder"}
	s := Slots{Shape: ShapeBlueGreen, Active: SlotA, Slots: map[string]SlotRecord{
		SlotA: {Unit: "socialweb-kcp-t-slot-a.service", Root: filepath.Join(dir, "slot-a"), Port: 40000, PID: 900}},
		Front: FrontRecord{Port: 45000}}
	if err := WriteSlots(r.slots, s); err != nil {
		t.Fatal(err)
	}
	r.cfg = RollConfig{SlotsFile: r.slots, Shared: r.shared,
		SpecFor: func(slot string) (hostsvc.UnitSpec, string, error) {
			root := filepath.Join(dir, "slot-"+slot)
			return hostsvc.UnitSpec{Name: "socialweb-kcp-t-slot-" + slot + ".service", Exec: []string{"/opt/kcp"}}, root, nil
		},
		ListenPort: func(ctx context.Context, pid int) (int, error) { return 41000 + pid - 1000, nil },
		Poll:       time.Millisecond, ReadyTimeout: time.Second, LeaseTimeout: time.Second, DrainTimeout: 10 * time.Millisecond,
	}
	return r
}

func (r *rig) roll(t *testing.T) (RollReport, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rep, _, err := Roll(ctx, r.m, r.f, r.a, r.cfg)
	return rep, err
}

func TestEverySlotRootGetsTheSharedKeyMaterialBeforeStart(t *testing.T) {
	r := newRig(t)
	r.m.onStart = func(spec hostsvc.UnitSpec) error {
		root := filepath.Join(r.dir, "slot-b")
		for _, f := range SharedFiles {
			b, err := os.ReadFile(filepath.Join(root, f))
			if err != nil || string(b) != "material "+f {
				return errors.New("slot b started without " + f)
			}
		}
		return nil
	}
	if _, err := r.roll(t); err != nil {
		t.Fatal(err)
	}
	r2 := newRig(t)
	os.Remove(filepath.Join(r2.shared, "admin.kubeconfig"))
	if _, err := r2.roll(t); err == nil || !strings.Contains(err.Error(), "admin.kubeconfig") {
		t.Fatalf("a roll with incomplete shared material: %v", err)
	}
	if r2.m.isRunning("socialweb-kcp-t-slot-b.service") {
		t.Fatal("slot b started without its shared material")
	}
}

func TestKcpSlotsJsonIsPersistedBeforeTheFrontFlip(t *testing.T) {
	r := newRig(t)
	r.f.onSet = func(backend string) error {
		s, err := ReadSlots(r.slots)
		if err != nil {
			return err
		}
		if s.Active != SlotB || s.Slots[SlotB].Port != 41001 {
			return errors.New("the front flipped before kcp-slots.json named slot b active")
		}
		return nil
	}
	rep, err := r.roll(t)
	if err != nil {
		t.Fatal(err)
	}
	if rep.From != SlotA || rep.To != SlotB || r.f.backend != "127.0.0.1:41001" {
		t.Fatalf("report %+v front %s", rep, r.f.backend)
	}
	s, _ := ReadSlots(r.slots)
	if s.Active != SlotB {
		t.Fatalf("active %s", s.Active)
	}
	if _, ok := s.Slots[SlotA]; ok {
		t.Fatalf("the stopped slot a is still recorded: %+v", s.Slots)
	}
}

func TestTheLeaseIsDeletedOnlyAfterTheOldSlotExited(t *testing.T) {
	r := newRig(t)
	r.a.onDelete = func() error {
		if r.m.isRunning("socialweb-kcp-t-slot-a.service") {
			return errors.New("the lease was deleted while the old slot ran")
		}
		return nil
	}
	rep, err := r.roll(t)
	if err != nil {
		t.Fatal(err)
	}
	flip, stop, del, probe := r.j.index("flip 127.0.0.1:41001"), r.j.index("stop socialweb-kcp-t-slot-a.service"),
		r.j.index("delete lease"), r.j.index("probe")
	if !(flip >= 0 && flip < stop && stop < del && del < probe) {
		t.Fatalf("order %v", r.j.events)
	}
	if rep.OldHolder != "old_holder" || rep.NewHolder != "new_holder" {
		t.Fatalf("holders %q -> %q", rep.OldHolder, rep.NewHolder)
	}
}

func TestReadinessIsReadOnTheNewSlotsOwnPortNeverThroughACopiedKubeconfig(t *testing.T) {
	r := newRig(t)
	r.a.readyPort = map[int]bool{40000: true, 45000: true}
	r.cfg.ReadyTimeout = 50 * time.Millisecond
	if _, err := r.roll(t); err == nil {
		t.Fatal("a roll flipped while only the old slot and the front answered /readyz")
	}
	for _, p := range r.a.asked {
		if p != 41001 {
			t.Fatalf("readiness was asked on port %d, not the new slot's own 41001", p)
		}
	}
	if r.f.backend != "127.0.0.1:40000" || r.m.isRunning("socialweb-kcp-t-slot-b.service") || !r.m.isRunning("socialweb-kcp-t-slot-a.service") {
		t.Fatalf("a failed roll left front %s, slot b running=%v, slot a running=%v", r.f.backend,
			r.m.isRunning("socialweb-kcp-t-slot-b.service"), r.m.isRunning("socialweb-kcp-t-slot-a.service"))
	}
	if s, _ := ReadSlots(r.slots); s.Active != SlotA {
		t.Fatalf("a failed roll moved the active slot to %s", s.Active)
	}
}

func TestOnListenFiresBeforeReadyWithTheSlotsOwnPort(t *testing.T) {
	r := newRig(t)
	var got []string
	r.cfg.OnListen = func(name string, addr net.Addr) {
		got = append(got, name+" "+addr.String())
		r.j.add("listen " + name)
	}
	if _, err := r.roll(t); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "kcp-b 127.0.0.1:41001" {
		t.Fatalf("OnListen %v", got)
	}
	if r.j.index("listen kcp-b") > r.j.index("flip 127.0.0.1:41001") {
		t.Fatalf("OnListen after the flip: %v", r.j.events)
	}
}

func TestABidderSlotArgvHasNoLeaderElectionAndThereIsNoSecondSlot(t *testing.T) {
	c := cfg()
	c.Shape = ShapeSingle
	c.FrontURL = "https://127.0.0.1:45000"
	argv, err := SlotArgv(c)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(argv, LeaderElection) {
		t.Fatalf("a bidder slot runs leader election: %v", argv)
	}
	if got := SlotNames(ShapeSingle); !slices.Equal(got, []string{SlotA}) {
		t.Fatalf("single-slot names %v", got)
	}
	r := newRig(t)
	s, _ := ReadSlots(r.slots)
	s.Shape = ShapeSingle
	WriteSlots(r.slots, s)
	if _, err := r.roll(t); !errors.Is(err, ErrSingleSlot) {
		t.Fatalf("a blue/green roll of a single-slot owner: %v", err)
	}
}

func TestTheSlotArgvCarriesLeaderElectionAndTheFrontURL(t *testing.T) {
	c := cfg()
	c.Shape = ShapeBlueGreen
	c.FrontURL = ""
	if _, err := SlotArgv(c); err == nil {
		t.Fatal("a blue/green slot without the front URL was accepted")
	}
	c.FrontURL = "https://127.0.0.1:45000"
	argv, err := SlotArgv(c)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, LeaderElection) || !slices.Contains(argv, "--shard-base-url=https://127.0.0.1:45000") {
		t.Fatalf("argv %v", argv)
	}
	var cut []string
	for _, a := range argv {
		if a != LeaderElection {
			cut = append(cut, a)
		}
	}
	if err := CheckArgv(cut, ShapeBlueGreen); err == nil {
		t.Fatal("a blue/green argv without leader election passed the check")
	}
}

func TestNoConsumerUsesTheVolatileKcpAdminContext(t *testing.T) {
	p := filepath.Join(t.TempDir(), "admin.kubeconfig")
	front := "https://127.0.0.1:45000"
	if err := WriteFrontKubeconfig(p, front, Admin{Token: "shard-token", CA: []byte("ca")}); err != nil {
		t.Fatal(err)
	}
	c, err := clientcmd.LoadFromFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Contexts) == 0 || c.Contexts[c.CurrentContext] == nil {
		t.Fatalf("no current context: %+v", c)
	}
	for name, ctx := range c.Contexts {
		if ctx.AuthInfo != ShardAdminUser {
			t.Errorf("context %s uses %s", name, ctx.AuthInfo)
		}
		if srv := c.Clusters[ctx.Cluster].Server; !strings.HasPrefix(srv, front) {
			t.Errorf("context %s names %s, not the front", name, srv)
		}
	}
	for name := range c.AuthInfos {
		if name != ShardAdminUser {
			t.Errorf("user %s is in the consumer kubeconfig", name)
		}
	}
}

func TestSaveSharedCopiesTheFiveFilesAndRefusesAPartialSet(t *testing.T) {
	dir := t.TempDir()
	root, shared := filepath.Join(dir, "slot-a"), filepath.Join(dir, "shared")
	os.MkdirAll(root, 0o700)
	for _, f := range SharedFiles[:4] {
		os.WriteFile(filepath.Join(root, f), []byte(f), 0o600)
	}
	if err := SaveShared(root, shared); err == nil {
		t.Fatal("a partial set was saved")
	}
	os.WriteFile(filepath.Join(root, "admin.kubeconfig"), []byte("kc"), 0o600)
	if err := SaveShared(root, shared); err != nil {
		t.Fatal(err)
	}
	if !HasShared(shared) {
		t.Fatal("shared material missing after save")
	}
	fi, _ := os.Stat(shared)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("shared dir mode %v", fi.Mode().Perm())
	}
}
