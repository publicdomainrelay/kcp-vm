//go:build linux

package hostsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

type fakeUnit struct {
	state      ActiveState
	result     string
	mainPID    int
	invocation string
	code       int32
	status     int32
}

type fakeBus struct {
	mu        sync.Mutex
	units     map[string]*fakeUnit
	calls     []string
	deadlines []bool
	started   [][]sd.Property
	version   string
	errs      map[string]error
	pidfdUnit string
	pidfdInv  string
	nextInv   int
	closed    bool
}

func newFakeBus() *fakeBus {
	return &fakeBus{units: map[string]*fakeUnit{}, errs: map[string]error{}, version: "259.9-1.fc44"}
}

func (f *fakeBus) record(ctx context.Context, call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := ctx.Deadline()
	f.calls = append(f.calls, call)
	f.deadlines = append(f.deadlines, ok)
	return f.errs[call]
}

func (f *fakeBus) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeBus) StartTransientUnit(ctx context.Context, name string, props []sd.Property, ch chan<- string) error {
	if err := f.record(ctx, "StartTransientUnit"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, props)
	if _, ok := f.units[name]; ok {
		return dbus.Error{Name: errUnitExists, Body: []any{"Unit " + name + " was already loaded or has a fragment file."}}
	}
	f.nextInv++
	f.units[name] = &fakeUnit{state: Active, result: "success", mainPID: 4000 + f.nextInv,
		invocation: fmt.Sprintf("%032x", f.nextInv)}
	ch <- "done"
	return nil
}

func (f *fakeBus) StopUnit(ctx context.Context, name string, ch chan<- string) error {
	if err := f.record(ctx, "StopUnit"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.units[name]
	if !ok {
		return dbus.Error{Name: errNoSuchUnit}
	}
	if u.state != Failed {
		delete(f.units, name)
	}
	ch <- "done"
	return nil
}

func (f *fakeBus) ResetFailedUnit(ctx context.Context, name string) error {
	if err := f.record(ctx, "ResetFailedUnit"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.units[name]; ok && u.state == Failed {
		delete(f.units, name)
	}
	return nil
}

func (f *fakeBus) UnitProperties(ctx context.Context, name string) (map[string]any, error) {
	if err := f.record(ctx, "UnitProperties"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.units[name]
	if !ok {
		return map[string]any{"Id": name, "LoadState": "not-found", "ActiveState": "inactive", "SubState": "dead",
			"InvocationID": []byte{}, "ActiveEnterTimestamp": uint64(0)}, nil
	}
	return map[string]any{"Id": name, "LoadState": "loaded", "ActiveState": string(u.state), "SubState": "running",
		"InvocationID": hexBytes(u.invocation), "ActiveEnterTimestamp": uint64(1760000000000000)}, nil
}

func hexBytes(s string) []byte {
	b := make([]byte, len(s)/2)
	for i := range b {
		fmt.Sscanf(s[2*i:2*i+2], "%02x", &b[i])
	}
	return b
}

func (f *fakeBus) ServiceProperties(ctx context.Context, name string) (map[string]any, error) {
	if err := f.record(ctx, "ServiceProperties"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.units[name]
	if !ok {
		return map[string]any{"MainPID": uint32(0), "Result": "success", "ExecMainCode": int32(0), "ExecMainStatus": int32(0)}, nil
	}
	return map[string]any{"MainPID": uint32(u.mainPID), "Result": u.result, "ExecMainCode": u.code, "ExecMainStatus": u.status}, nil
}

func (f *fakeBus) ListUnitsByPatterns(ctx context.Context, patterns []string) ([]sd.UnitStatus, error) {
	if err := f.record(ctx, "ListUnitsByPatterns"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sd.UnitStatus
	for name, u := range f.units {
		out = append(out, sd.UnitStatus{Name: name, LoadState: "loaded", ActiveState: string(u.state)})
	}
	out = append(out, sd.UnitStatus{Name: "socialweb-vm-aaaa-gone.service", LoadState: "not-found", ActiveState: "inactive"})
	return out, nil
}

func (f *fakeBus) UnitByPIDFD(ctx context.Context, fd int) (string, string, error) {
	if err := f.record(ctx, "UnitByPIDFD"); err != nil {
		return "", "", err
	}
	return f.pidfdUnit, f.pidfdInv, nil
}

func (f *fakeBus) Version(ctx context.Context) (string, error) {
	if err := f.record(ctx, "Version"); err != nil {
		return "", err
	}
	return f.version, nil
}

func (f *fakeBus) ManagerStarted(ctx context.Context) (uint64, error) {
	if err := f.record(ctx, "ManagerStarted"); err != nil {
		return 0, err
	}
	return 44216348, nil
}

func (f *fakeBus) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

type fakePin struct{ ticks uint64 }

func (p fakePin) FD() int { return 99 }

func (p fakePin) StartTicks() uint64 { return p.ticks }

func (p fakePin) Close() {}

type fakeProcs struct {
	boot  string
	ticks map[int]uint64
	err   error
}

func (p *fakeProcs) Pin(pid int) (pinned, error) {
	if p.err != nil {
		return nil, p.err
	}
	t, ok := p.ticks[pid]
	if !ok {
		return nil, fmt.Errorf("%w: pid %d", errProcessGone, pid)
	}
	return fakePin{ticks: t}, nil
}

func (p *fakeProcs) BootID() (string, error) { return p.boot, nil }

func newTestManager(b *fakeBus, p *fakeProcs) *systemd {
	return newManager(func(ctx context.Context) (bus, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("dial without a deadline")
		}
		return b, nil
	}, p, time.Second)
}

func vmSpec(name string) UnitSpec {
	return UnitSpec{
		Name:        name,
		Description: "stand-in",
		Exec:        []string{"/usr/bin/sleep", "infinity"},
		Limits:      Limits{MemoryMax: 64 << 20, CPUWeight: 50, CPUQuotaPercent: 20, TasksMax: 16},
		KillSignal:  syscall.SIGKILL,
		StopTimeout: 10 * time.Second,
		After:       []string{"socialweb-pg-aaaa-db.service"},
		Output:      Output{StdoutFile: "/tmp/console.log", StderrFile: "/tmp/vmm.log"},
	}
}

func propMap(props []sd.Property) map[string]any {
	m := map[string]any{}
	for _, p := range props {
		m[p.Name] = p.Value.Value()
	}
	return m
}

func TestExecStartIsNeverIgnoreFailure(t *testing.T) {
	props := unitProperties(vmSpec("socialweb-vm-aaaa-one.service"))
	v, ok := propMap(props)["ExecStart"]
	if !ok {
		t.Fatalf("no ExecStart among %v", props)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice || rv.Len() != 1 {
		t.Fatalf("ExecStart is %#v, want one command", v)
	}
	if rv.Index(0).FieldByName("UncleanIsFailure").Bool() {
		t.Fatalf("ExecStart sends ignore_failure=true: a SIGKILLed child would report success and its unit be collected")
	}
	if got := rv.Index(0).FieldByName("Path").String(); got != "/usr/bin/sleep" {
		t.Fatalf("ExecStart path %q", got)
	}
}

func TestUnitPropertiesKeepAFailedUnitAndSwapZeroAndOfferNoUnenforcedLimit(t *testing.T) {
	m := propMap(unitProperties(vmSpec("socialweb-vm-aaaa-one.service")))
	want := map[string]any{
		"Type":                       "exec",
		"Restart":                    "no",
		"CollectMode":                "inactive",
		"KillMode":                   "control-group",
		"KillSignal":                 int32(syscall.SIGKILL),
		"TimeoutStopUSec":            uint64(10_000_000),
		"MemoryMax":                  uint64(64 << 20),
		"MemorySwapMax":              uint64(0),
		"OOMPolicy":                  "stop",
		"TasksMax":                   uint64(16),
		"CPUWeight":                  uint64(50),
		"CPUQuotaPerSecUSec":         uint64(200_000),
		"Slice":                      DefaultSlice,
		"StandardOutputFileToAppend": "/tmp/console.log",
		"StandardErrorFileToAppend":  "/tmp/vmm.log",
	}
	for k, v := range want {
		if !reflect.DeepEqual(m[k], v) {
			t.Errorf("%s = %#v, want %#v", k, m[k], v)
		}
	}
	for _, k := range []string{"AllowedCPUs", "IOWeight", "ManagedOOMPreference", "MemoryHigh"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s is set: the user manager does not enforce it (linux-proofs E2)", k)
		}
	}
	if !reflect.DeepEqual(m["After"], []string{"socialweb-pg-aaaa-db.service"}) {
		t.Errorf("After = %#v", m["After"])
	}
	noMem := propMap(unitProperties(UnitSpec{Name: "socialweb-x-a-b.service", Exec: []string{"/bin/true"}}))
	if _, ok := noMem["MemorySwapMax"]; ok {
		t.Errorf("MemorySwapMax set without MemoryMax")
	}
}

func TestStartRefusesANameOutsideThePrefixAndAnAppSlice(t *testing.T) {
	b := newFakeBus()
	m := newTestManager(b, &fakeProcs{})
	for _, spec := range []UnitSpec{
		{Name: "swproof-vm-1.service", Exec: []string{"/usr/bin/sleep"}},
		{Name: "socialweb-vm-a-b.scope", Exec: []string{"/usr/bin/sleep"}},
		{Name: "socialweb-vm-a-b.service", Exec: []string{"sleep"}},
		{Name: "socialweb-vm-a-b.service", Exec: []string{"/usr/bin/sleep"}, Slice: "app.slice"},
	} {
		if _, err := m.Start(context.Background(), spec); err == nil {
			t.Errorf("Start(%+v) succeeded", spec)
		}
	}
	if n := b.count("StartTransientUnit"); n != 0 {
		t.Fatalf("%d StartTransientUnit calls for refused specs", n)
	}
}

func TestAnExistingUnitNameIsAdoptedNotRestarted(t *testing.T) {
	b := newFakeBus()
	m := newTestManager(b, &fakeProcs{})
	name := "socialweb-vm-aaaa-one.service"
	first, err := m.Start(context.Background(), vmSpec(name))
	if err != nil {
		t.Fatal(err)
	}
	if first.Adopted || first.State != Active || first.InvocationID == "" {
		t.Fatalf("first start: %+v", first)
	}
	second, err := m.Start(context.Background(), vmSpec(name))
	if err != nil {
		t.Fatalf("second start of an existing name: %v", err)
	}
	if !second.Adopted {
		t.Fatalf("second start not marked adopted: %+v", second)
	}
	if second.InvocationID != first.InvocationID || second.MainPID != first.MainPID {
		t.Fatalf("adoption changed the unit: %+v -> %+v", first, second)
	}
	for _, call := range []string{"StopUnit", "ResetFailedUnit"} {
		if n := b.count(call); n != 0 {
			t.Fatalf("adoption called %s %d times", call, n)
		}
	}
	b.units[name].state = Failed
	b.units[name].result = "signal"
	third, err := m.Start(context.Background(), vmSpec(name))
	if err != nil || !third.Adopted || third.State != Failed || third.Result != "signal" {
		t.Fatalf("a failed unit must be adopted as failed, not re-created: %+v %v", third, err)
	}
}

func TestEveryCallCarriesADeadline(t *testing.T) {
	b := newFakeBus()
	p := &fakeProcs{boot: "boot-1", ticks: map[int]uint64{}}
	m := newTestManager(b, p)
	ctx := context.Background()
	name := "socialweb-vm-aaaa-one.service"
	st, err := m.Start(ctx, vmSpec(name))
	if err != nil {
		t.Fatal(err)
	}
	p.ticks[st.MainPID] = 77
	b.pidfdUnit, b.pidfdInv = name, st.InvocationID
	id, err := m.Identify(ctx, name, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Check(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(ctx, "socialweb-vm-aaaa-"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetFailed(ctx, name); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) < 8 {
		t.Fatalf("only %d calls recorded: %v", len(b.calls), b.calls)
	}
	for i, ok := range b.deadlines {
		if !ok {
			t.Errorf("call %d (%s) carried no deadline", i, b.calls[i])
		}
	}
}

func TestOnlyDialAuthAndTimeoutAreUnreachable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"dial ENOENT", dialError{&net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.ENOENT)}}, true},
		{"dial ECONNREFUSED", dialError{os.NewSyscallError("connect", syscall.ECONNREFUSED)}, true},
		{"auth", dialError{errors.New("dbus: authentication failed")}, true},
		{"deadline", context.DeadlineExceeded, true},
		{"connection closed", dbus.ErrClosed, true},
		{"read EOF", io.EOF, true},
		{"reset", &net.OpError{Op: "read", Net: "unix", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, true},
		{"NoSuchUnit", dbus.Error{Name: errNoSuchUnit}, false},
		{"UnixProcessIdUnknown", dbus.Error{Name: errUnixProcessIDUnknown}, false},
		{"NoUnitForPID", dbus.Error{Name: errNoUnitForPID}, false},
		{"AccessDenied", dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}, false},
		{"decode", errors.New("dbus: store: type mismatch"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newFakeBus()
			b.errs["UnitProperties"] = c.err
			m := newTestManager(b, &fakeProcs{})
			_, err := m.Inspect(context.Background(), "socialweb-vm-aaaa-one.service")
			if got := errors.Is(err, ErrUnreachable); got != c.want {
				t.Fatalf("Inspect error %v: unreachable=%v, want %v", err, got, c.want)
			}
			if c.want && !b.closed {
				t.Fatalf("a transport error left the connection in use")
			}
		})
	}
	m := newManager(func(ctx context.Context) (bus, error) {
		return nil, dialError{errors.New("connect: no such file or directory")}
	}, &fakeProcs{}, time.Second)
	if _, err := m.Inspect(context.Background(), "socialweb-vm-aaaa-one.service"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a dial failure is %v, want ErrUnreachable", err)
	}
}

func TestTheRealDialerEndsAtTheDeadlineOnASilentSocketAndIsUnreachable(t *testing.T) {
	dir, err := os.MkdirTemp("", "hs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	missing := filepath.Join(dir, "missing")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := dialPrivate(missing)(ctx); err == nil || !unreachable(err) {
		t.Fatalf("dial of a missing socket: %v", err)
	}
	silent := filepath.Join(dir, "silent")
	l, err := net.Listen("unix", silent)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var held []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	t0 := time.Now()
	_, err = dialPrivate(silent)(ctx2)
	took := time.Since(t0)
	if err == nil || !unreachable(err) {
		t.Fatalf("dial of a silent socket: %v", err)
	}
	if took > 2*time.Second {
		t.Fatalf("dial of a silent socket took %s past a 300ms deadline", took)
	}
}

func TestOpenRefusesSystemdOlderThan253(t *testing.T) {
	for v, ok := range map[string]bool{"252.1-1.el9": false, "253": true, "259.9-1.fc44": true, "garbage": false} {
		b := newFakeBus()
		b.version = v
		m := newTestManager(b, &fakeProcs{})
		err := m.checkVersion(context.Background())
		if ok && err != nil {
			t.Errorf("version %q refused: %v", v, err)
		}
		if !ok && !errors.Is(err, ErrSystemdTooOld) {
			t.Errorf("version %q: %v, want ErrSystemdTooOld", v, err)
		}
	}
}

func TestListNeverReturnsAnotherPrefixOrANotFoundUnit(t *testing.T) {
	b := newFakeBus()
	m := newTestManager(b, &fakeProcs{})
	ctx := context.Background()
	for _, n := range []string{"socialweb-vm-aaaa-one.service", "socialweb-vm-aaaa-two.service",
		"socialweb-vm-aaaab-x.service", "socialweb-vm-bbbb-one.service", "socialweb-bed-1-vm-aaaa-one.service"} {
		if _, err := m.Start(ctx, vmSpec(n)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.List(ctx, "socialweb-vm-aaaa-")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "socialweb-vm-aaaa-one.service,socialweb-vm-aaaa-two.service" {
		t.Fatalf("List returned %v", names)
	}
	for _, bad := range []string{"", "socialweb-", "socialweb-vm-*", "swproof-"} {
		if _, err := m.List(ctx, bad); err == nil {
			t.Errorf("List(%q) accepted", bad)
		}
	}
}

func TestCheckVerdicts(t *testing.T) {
	const name = "socialweb-vm-aaaa-one.service"
	timeout := fmt.Errorf("call: %w", context.DeadlineExceeded)
	cases := []struct {
		name    string
		setup   func(b *fakeBus, p *fakeProcs, want *Identity)
		verdict Verdict
		reason  LostReason
		stopped bool
		err     bool
	}{
		{"boot id changed", func(b *fakeBus, p *fakeProcs, w *Identity) { p.boot = "boot-2" }, VerdictLost, LostHostRebooted, false, false},
		{"bus timeout", func(b *fakeBus, p *fakeProcs, w *Identity) { b.errs["UnitProperties"] = timeout }, VerdictUnknown, "", false, false},
		{"unit gone", func(b *fakeBus, p *fakeProcs, w *Identity) { delete(b.units, name) }, VerdictLost, LostUnitGone, false, false},
		{"unit failed", func(b *fakeBus, p *fakeProcs, w *Identity) {
			b.units[name].state, b.units[name].result, b.units[name].code, b.units[name].status = Failed, "signal", 2, 9
		}, VerdictLost, LostUnitFailed, false, false},
		{"oom killed", func(b *fakeBus, p *fakeProcs, w *Identity) {
			b.units[name].state, b.units[name].result = Failed, "oom-kill"
		}, VerdictLost, LostOOMKilled, false, false},
		{"exited", func(b *fakeBus, p *fakeProcs, w *Identity) { b.units[name].state = Inactive }, VerdictLost, LostUnitExited, false, false},
		{"other invocation", func(b *fakeBus, p *fakeProcs, w *Identity) { w.InvocationID = strings.Repeat("e", 32) }, VerdictLost, LostIdentityMismatch, true, false},
		{"pid gone", func(b *fakeBus, p *fakeProcs, w *Identity) { delete(p.ticks, w.PID) }, VerdictLost, LostIdentityMismatch, true, false},
		{"pid reused", func(b *fakeBus, p *fakeProcs, w *Identity) { p.ticks[w.PID] = 12345 }, VerdictLost, LostIdentityMismatch, true, false},
		{"pidfd unknown", func(b *fakeBus, p *fakeProcs, w *Identity) {
			b.errs["UnitByPIDFD"] = dbus.Error{Name: errUnixProcessIDUnknown}
		}, VerdictLost, LostIdentityMismatch, true, false},
		{"pidfd in another unit", func(b *fakeBus, p *fakeProcs, w *Identity) { b.pidfdUnit = "socialweb-vm-aaaa-two.service" }, VerdictLost, LostIdentityMismatch, true, false},
		{"pidfd timeout", func(b *fakeBus, p *fakeProcs, w *Identity) { b.errs["UnitByPIDFD"] = timeout }, VerdictUnknown, "", false, false},
		{"pidfd access denied", func(b *fakeBus, p *fakeProcs, w *Identity) {
			b.errs["UnitByPIDFD"] = dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}
		}, VerdictUnknown, "", false, true},
		{"pin EMFILE", func(b *fakeBus, p *fakeProcs, w *Identity) { p.err = syscall.EMFILE }, VerdictUnknown, "", false, true},
		{"all match", func(b *fakeBus, p *fakeProcs, w *Identity) {}, VerdictRunning, "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newFakeBus()
			p := &fakeProcs{boot: "boot-1", ticks: map[int]uint64{}}
			m := newTestManager(b, p)
			ctx := context.Background()
			st, err := m.Start(ctx, vmSpec(name))
			if err != nil {
				t.Fatal(err)
			}
			p.ticks[st.MainPID] = 777
			b.pidfdUnit, b.pidfdInv = name, st.InvocationID
			want, err := m.Identify(ctx, name, 0)
			if err != nil {
				t.Fatal(err)
			}
			if want.PID != st.MainPID || want.StartTicks != 777 || want.BootID != "boot-1" || want.InvocationID != st.InvocationID {
				t.Fatalf("Identify: %+v", want)
			}
			stopsBefore := b.count("StopUnit")
			callsBefore := len(b.calls)
			c.setup(b, p, &want)
			obs, err := m.Check(ctx, want)
			if c.err != (err != nil) {
				t.Fatalf("Check error %v, want error=%v", err, c.err)
			}
			if obs.Verdict != c.verdict || obs.Reason != c.reason {
				t.Fatalf("Check = %s/%s (%s), want %s/%s", obs.Verdict, obs.Reason, obs.Detail, c.verdict, c.reason)
			}
			stopped := b.count("StopUnit") > stopsBefore
			if stopped != c.stopped || obs.Stopped != c.stopped {
				t.Fatalf("stopped=%v obs.Stopped=%v, want %v", stopped, obs.Stopped, c.stopped)
			}
			if c.reason == LostHostRebooted && len(b.calls) != callsBefore {
				t.Fatalf("a boot id change touched the bus: %v", b.calls[callsBefore:])
			}
		})
	}
}

func TestATransportErrorIsUnknownAndRedialsNextCall(t *testing.T) {
	dials := 0
	b := newFakeBus()
	m := newManager(func(ctx context.Context) (bus, error) {
		dials++
		b.closed = false
		return b, nil
	}, &fakeProcs{}, time.Second)
	b.errs["UnitProperties"] = dbus.ErrClosed
	if _, err := m.Inspect(context.Background(), "socialweb-vm-aaaa-one.service"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("%v", err)
	}
	delete(b.errs, "UnitProperties")
	st, err := m.Inspect(context.Background(), "socialweb-vm-aaaa-one.service")
	if err != nil || st.State != NotFound {
		t.Fatalf("after redial: %+v %v", st, err)
	}
	if dials != 2 {
		t.Fatalf("%d dials, want 2", dials)
	}
}

func TestOpenRefusesAnEmptyBackend(t *testing.T) {
	_, err := Open(context.Background(), Config{})
	if !errors.Is(err, ErrNoBackend) || !strings.Contains(err.Error(), "host-backend") {
		t.Fatalf("Open with no backend: %v, want ErrNoBackend naming the option", err)
	}
	if _, err := Open(context.Background(), Config{Backend: "systemd"}); err == nil {
		t.Fatalf("Open accepted an unknown backend")
	}
}

func TestAProcIdentityIsLostOnSystemdUser(t *testing.T) {
	m := newManager(func(ctx context.Context) (bus, error) { return newFakeBus(), nil }, &fakeProcs{boot: "b"}, time.Second)
	obs, err := m.Check(context.Background(), Identity{Unit: "socialweb-vm-aaaa-one.service", BootID: "b", Backend: BackendProc})
	if err != nil || obs.Verdict != VerdictLost || obs.Reason != LostIdentityMismatch || !strings.Contains(obs.Detail, "proc") {
		t.Fatalf("a proc identity on systemd-user reads %+v %v", obs, err)
	}
}
