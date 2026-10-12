package treeaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

type fakeHost struct {
	t      *testing.T
	root   string
	host   Host
	pinned map[string]string
	bins   map[string]string
}

func newFakeHost(t *testing.T) *fakeHost {
	root := t.TempDir()
	f := &fakeHost{t: t, root: root, host: Host{ProcRoot: filepath.Join(root, "proc"), CgroupRoot: filepath.Join(root, "cgroup")},
		pinned: map[string]string{}, bins: map[string]string{}}
	for _, name := range []string{"pasta", "firecracker", "socialweb-computer", "socialweb-vmexec", "sh"} {
		p := filepath.Join(root, "bin", name)
		mustWrite(t, p, "#!binary "+name+"\n")
		f.bins[name] = p
	}
	for _, name := range []string{"pasta", "firecracker"} {
		f.pinned[sum(t, f.bins[name])] = name
	}
	return f
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func sum(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (f *fakeHost) proc(pid, pgid int, bin, cgroup string, env ...string) {
	dir := filepath.Join(f.host.ProcRoot, strconv.Itoa(pid))
	mustWrite(f.t, filepath.Join(dir, "stat"), strconv.Itoa(pid)+" (x y) S 1 "+strconv.Itoa(pgid)+" 0 0\n")
	mustWrite(f.t, filepath.Join(dir, "cgroup"), "0::"+cgroup+"\n")
	mustWrite(f.t, filepath.Join(dir, "environ"), strings.Join(env, "\x00"))
	if err := os.Symlink(f.bins[bin], filepath.Join(dir, "exe")); err != nil {
		f.t.Fatal(err)
	}
	if cgroup != "" {
		procs := filepath.Join(f.host.CgroupRoot, cgroup, "cgroup.procs")
		old, _ := os.ReadFile(procs)
		mustWrite(f.t, procs, string(old)+strconv.Itoa(pid)+"\n")
	}
}

type fakeManager struct {
	backend hostsvc.Backend
	units   []hostsvc.UnitStatus
}

func (m fakeManager) List(ctx context.Context, prefix string) ([]hostsvc.UnitStatus, error) {
	if err := hostsvc.ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	var out []hostsvc.UnitStatus
	for _, u := range m.units {
		if strings.HasPrefix(u.Name, prefix) {
			out = append(out, u)
		}
	}
	return out, nil
}

func (m fakeManager) Capabilities() hostsvc.Capabilities {
	return hostsvc.Capabilities{Backend: m.backend}
}

const (
	vmUnit  = "socialweb-vm-0123456789-guest1.service"
	vmGroup = "/user.slice/user-990.slice/user@990.service/socialweb.slice/socialweb-b.slice/socialweb-b-0123456789.slice/" + vmUnit
)

func systemdUnit(state hostsvc.ActiveState) fakeManager {
	return fakeManager{backend: hostsvc.BackendSystemdUser,
		units: []hostsvc.UnitStatus{{Name: vmUnit, State: state, MainPID: 100},
			{Name: "socialweb-bao-0123456789.service", State: hostsvc.Active, MainPID: 50}}}
}

func audit(t *testing.T, f *fakeHost, m fakeManager, devsup int) Report {
	t.Helper()
	r, err := Audit(context.Background(), m, f.host, devsup, f.pinned)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAVMUnitRunningOnlyPinnedPastaAndFirecrackerPasses(t *testing.T) {
	f := newFakeHost(t)
	f.proc(100, 100, "pasta", vmGroup)
	f.proc(101, 100, "firecracker", vmGroup)
	f.proc(50, 50, "socialweb-computer", "/user.slice/user-990.slice/user@990.service/socialweb.slice/socialweb-bao-0123456789.service")
	r := audit(t, f, systemdUnit(hostsvc.Active), 0)
	if !r.Pass() || len(r.Units) != 1 || len(r.Units[0].Procs) != 2 {
		t.Fatalf("report %+v", r)
	}
}

func TestACoreBinaryInAVMUnitFails(t *testing.T) {
	f := newFakeHost(t)
	f.proc(100, 100, "pasta", vmGroup)
	f.proc(101, 100, "socialweb-computer", vmGroup+"/sub")
	r := audit(t, f, systemdUnit(hostsvc.Active), 0)
	if r.Pass() || len(r.Findings) != 1 || r.Findings[0].PID != 101 || !strings.HasSuffix(r.Findings[0].Exe, "socialweb-computer") {
		t.Fatalf("a core binary in a child cgroup of the VM unit: findings %+v", r.Findings)
	}
}

func TestAVmexecLeftAfterActivatingFails(t *testing.T) {
	for _, bin := range []string{"socialweb-vmexec", "sh"} {
		f := newFakeHost(t)
		f.proc(100, 100, bin, vmGroup)
		f.proc(101, 100, "firecracker", vmGroup)
		r := audit(t, f, systemdUnit(hostsvc.Active), 0)
		if r.Pass() || r.Findings[0].PID != 100 || !strings.Contains(r.Findings[0].Why, "not a pinned VM-unit asset") {
			t.Fatalf("%s left in an active VM unit: findings %+v", bin, r.Findings)
		}
	}
	f := newFakeHost(t)
	f.proc(100, 100, "socialweb-vmexec", vmGroup)
	r := audit(t, f, systemdUnit(hostsvc.Activating), 0)
	if r.Pass() || !strings.Contains(r.Findings[0].Why, "activating") {
		t.Fatalf("an activating VM unit passed: %+v", r)
	}
}

func TestAMainPidOutsideTheUnitsCgroupFails(t *testing.T) {
	f := newFakeHost(t)
	f.proc(100, 100, "pasta", "/user.slice/user-990.slice/user@990.service/app.slice/other.service")
	r := audit(t, f, systemdUnit(hostsvc.Active), 0)
	if r.Pass() || !strings.Contains(r.Findings[0].Why, "not inside the unit's cgroup") {
		t.Fatalf("findings %+v", r.Findings)
	}
}

func TestOnTheProcBackendDevsupOutsideTheGroupIsNotAMemberAndAnAttributedCoreFails(t *testing.T) {
	f := newFakeHost(t)
	m := fakeManager{backend: hostsvc.BackendProc, units: []hostsvc.UnitStatus{{Name: vmUnit, State: hostsvc.Active, MainPID: 100, InvocationID: "inv1"}}}
	f.proc(10, 10, "socialweb-computer", "", EnvUnit+"=x")
	f.proc(100, 100, "pasta", "", EnvUnit+"="+vmUnit, EnvInvoc+"=inv1")
	f.proc(101, 100, "firecracker", "")
	if r := audit(t, f, m, 10); !r.Pass() || len(r.Units[0].Procs) != 2 {
		t.Fatalf("devsup outside the group: %+v", r)
	}
	f.proc(102, 102, "socialweb-computer", "", EnvUnit+"="+vmUnit, EnvInvoc+"=inv1")
	f.proc(103, 103, "socialweb-computer", "", EnvUnit+"="+vmUnit, EnvInvoc+"=old")
	r := audit(t, f, m, 10)
	if r.Pass() || len(r.Findings) != 1 || r.Findings[0].PID != 102 {
		t.Fatalf("an attributed setsid descendant running the core: findings %+v", r.Findings)
	}
}

func TestTheMarketNetworkVMPrefixIsAudited(t *testing.T) {
	for name, want := range map[string]bool{
		"socialweb-vm-a-b.service": true, "socialweb-market-network-x1-vm-a.service": true,
		"socialweb-market-network-x1-kcp-a.service": false, "socialweb-bao-a.service": false,
	} {
		if IsVMUnit(name) != want {
			t.Errorf("IsVMUnit(%s) = %v", name, !want)
		}
	}
}

func TestOnlyAVerifiedRuntimeSetPinsItsVMExecutables(t *testing.T) {
	root := t.TempDir()
	good := strings.Repeat("a", 64)
	bad := strings.Repeat("b", 64)
	mustWrite(t, filepath.Join(root, good, "pasta"), "pasta-good")
	mustWrite(t, filepath.Join(root, bad, "pasta"), "pasta-tampered")
	mustWrite(t, filepath.Join(root, good, "kine"), "kine")
	pins := []versions.Pin{{Name: "pasta", Executables: map[string]string{"pasta": "pasta"}},
		{Name: "kine", Executables: map[string]string{"kine": "kine"}}}
	digest := func(dir string) (string, error) { return good, nil }
	got, notes := Pinned(root, pins, digest)
	if len(got) != 1 || got[sum(t, filepath.Join(root, good, "pasta"))] == "" {
		t.Fatalf("pinned %v notes %v", got, notes)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], bad) {
		t.Fatalf("the tampered set was not named: %v", notes)
	}
}
