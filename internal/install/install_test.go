package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func fakeBinary(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "socialweb-computer")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func opts(t *testing.T, root string, role hostcontract.Role, handles ...string) Options {
	if role == hostcontract.RoleGateway {
		handles = nil
	}
	return Options{Role: role, Handles: handles, Root: root, Binary: fakeBinary(t, "core v1"), Out: &bytes.Buffer{}}
}

func ctx() context.Context { return context.Background() }

func TestInstallTwiceChangesNothingTheSecondTime(t *testing.T) {
	for _, role := range []hostcontract.Role{hostcontract.RoleGateway, hostcontract.RoleBidder} {
		t.Run(string(role), func(t *testing.T) {
			sys, root := newFakeHost(t, true)
			o := opts(t, root, role, "alice.example.com")
			first, err := Install(ctx(), o, sys)
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Changes) == 0 || sys.checks != 1 {
				t.Fatalf("first run changed nothing or skipped check-host: %+v checks %d", first.Changes, sys.checks)
			}
			before := len(sys.mutations)
			second, err := Install(ctx(), o, sys)
			if err != nil {
				t.Fatal(err)
			}
			if len(second.Changes) != 0 || len(sys.mutations) != before {
				t.Fatalf("second run changed %v and ran %v", second.Changes, sys.mutations[before:])
			}
			for _, s := range second.Steps {
				if s.Outcome == OutcomeCreated || s.Outcome == OutcomeUpdated {
					t.Errorf("second run step %s: %s", s.Name, s.Outcome)
				}
			}
			if sys.checks != 2 {
				t.Errorf("check-host ran %d times over two installs, want 2", sys.checks)
			}
		})
	}
}

func TestARerunWithAnotherHandleAddsOnlyThatInstance(t *testing.T) {
	sys, root := newFakeHost(t, false)
	o := opts(t, root, hostcontract.RoleBidder, "alice.example.com")
	if _, err := Install(ctx(), o, sys); err != nil {
		t.Fatal(err)
	}
	before := len(sys.mutations)
	o.Handles = []string{"alice.example.com", "bob.example.com"}
	r, err := Install(ctx(), o, sys)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sys.mutations[before:], "|")
	if !strings.Contains(got, "enable --now socialweb-computer-bidder@bob.example.com.service") ||
		strings.Contains(got, "alice") || strings.Contains(got, "useradd") || strings.Contains(got, "linger") {
		t.Fatalf("adding bob ran %v (changes %v)", sys.mutations[before:], r.Changes)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range []string{"etc/passwd", "etc/group", "etc/subuid", "etc/subgid",
		"etc/systemd/system/user@990.service.d/socialweb-delegate.conf", "etc/apparmor.d/socialweb-pasta",
		"var/lib/systemd/linger/swc-bidder", "var/lib/systemd/linger/socialweb", "etc/rancher/node/password",
		"sys/kernel/security/apparmor/profiles", "var/lib/socialweb-computer/install-swc-bidder.json",
		"var/lib/socialweb-computer/install-socialweb.json"} {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			out[p] = "<absent>"
			continue
		}
		out[p] = string(b)
	}
	for _, d := range []string{"etc/systemd/system/user@990.service.d", "etc/rancher", "var/lib/socialweb-computer/swc-bidder",
		"var/lib/socialweb-computer/socialweb"} {
		if _, err := os.Stat(filepath.Join(root, d)); err == nil {
			out[d+"/"] = "<dir>"
		} else {
			out[d+"/"] = "<absent>"
		}
	}
	return out
}

func TestUninstallPurgeRestoresEveryHostFileInstallTouched(t *testing.T) {
	for _, role := range []hostcontract.Role{hostcontract.RoleGateway, hostcontract.RoleBidder} {
		t.Run(string(role), func(t *testing.T) {
			sys, root := newFakeHost(t, true)
			pre := snapshot(t, root)
			if _, err := Install(ctx(), opts(t, root, role, "alice.example.com"), sys); err != nil {
				t.Fatal(err)
			}
			if mid := snapshot(t, root); mid["etc/passwd"] == pre["etc/passwd"] {
				t.Fatal("install did not create the account")
			}
			if _, err := Uninstall(ctx(), UninstallOptions{Role: role, Root: root, Purge: true, Out: &bytes.Buffer{}}, sys); err != nil {
				t.Fatal(err)
			}
			post := snapshot(t, root)
			for k, v := range pre {
				if post[k] != v {
					t.Errorf("%s after uninstall --purge:\n%q\nbefore install:\n%q", k, post[k], v)
				}
			}
		})
	}
}

func TestUninstallKeepsWhatInstallFoundPresent(t *testing.T) {
	sys, root := newFakeHost(t, false)
	if err := sys.CreateAccount(AccountBidder, "/var/lib/socialweb-computer/"+AccountBidder); err != nil {
		t.Fatal(err)
	}
	if err := sys.AddToGroup(AccountBidder, "kvm"); err != nil {
		t.Fatal(err)
	}
	sys.write("etc/subuid", sys.read("etc/subuid")+"swc-bidder:2000000:16777216\n")
	pre := snapshot(t, root)
	o := opts(t, root, hostcontract.RoleBidder, "alice.example.com")
	if _, err := Install(ctx(), o, sys); !errors.Is(err, ErrForeignAccount) {
		t.Fatalf("install over an account it did not create: %v, want ErrForeignAccount", err)
	}
	o.AdoptAccount = true
	if _, err := Install(ctx(), o, sys); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(ctx(), UninstallOptions{Role: hostcontract.RoleBidder, Root: root, Purge: true, Out: &bytes.Buffer{}}, sys); err != nil {
		t.Fatal(err)
	}
	post := snapshot(t, root)
	for _, k := range []string{"etc/passwd", "etc/group", "etc/subuid"} {
		if post[k] != pre[k] {
			t.Errorf("%s: what install found present was removed:\n%q\nwant\n%q", k, post[k], pre[k])
		}
	}
	if post["etc/subgid"] != baseSubgid {
		t.Errorf("the subgid line install created was not removed: %q", post["etc/subgid"])
	}
}

func TestUninstallWithoutPurgeKeepsTheAccountAndState(t *testing.T) {
	sys, root := newFakeHost(t, false)
	if _, err := Install(ctx(), opts(t, root, hostcontract.RoleGateway), sys); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(ctx(), UninstallOptions{Role: hostcontract.RoleGateway, Root: root, Out: &bytes.Buffer{}}, sys); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := sys.LookupAccount(AccountGateway); !ok {
		t.Fatal("uninstall without --purge removed the account")
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/socialweb-computer/socialweb/bin/current")); err != nil {
		t.Fatalf("uninstall without --purge removed the state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/systemd/linger/socialweb")); err == nil {
		t.Fatal("linger is still on")
	}
	before := len(sys.mutations)
	if _, err := Install(ctx(), opts(t, root, hostcontract.RoleGateway), sys); err != nil {
		t.Fatalf("re-install after a plain uninstall: %v", err)
	}
	if strings.Contains(strings.Join(sys.mutations[before:], "|"), "useradd") {
		t.Fatal("re-install created the kept account again")
	}
}

func TestUninstallRefusesWhileAVMUnitIsActive(t *testing.T) {
	sys, root := newFakeHost(t, false)
	if _, err := Install(ctx(), opts(t, root, hostcontract.RoleBidder, "alice.example.com"), sys); err != nil {
		t.Fatal(err)
	}
	sys.units["socialweb-vm-0123456789-guest1.service"] = true
	sys.units["socialweb-bao-0123456789.service"] = true
	before := len(sys.mutations)
	_, err := Uninstall(ctx(), UninstallOptions{Role: hostcontract.RoleBidder, Root: root, Out: &bytes.Buffer{}}, sys)
	if !errors.Is(err, ErrVMsRunning) || !strings.Contains(err.Error(), "socialweb-vm-0123456789-guest1.service") {
		t.Fatalf("uninstall with an active VM unit: %v", err)
	}
	if len(sys.mutations) != before {
		t.Fatalf("a refused uninstall still ran %v", sys.mutations[before:])
	}
	if _, err := Uninstall(ctx(), UninstallOptions{Role: hostcontract.RoleBidder, Root: root, Force: true, Out: &bytes.Buffer{}}, sys); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sys.mutations[before:], "|")
	for _, want := range []string{"stop socialweb-vm-0123456789-guest1.service", "stop socialweb-bao-0123456789.service", "disable-linger"} {
		if !strings.Contains(got, want) {
			t.Errorf("uninstall --force did not %s: %v", want, sys.mutations[before:])
		}
	}
	if strings.Index(got, "stop socialweb-vm") > strings.Index(got, "disable-linger") {
		t.Error("linger was disabled before the runtime units were stopped")
	}
}

func indexOf(t *testing.T, list []string, prefix string) int {
	t.Helper()
	for i, s := range list {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	t.Fatalf("%q never ran: %v", prefix, list)
	return -1
}

func TestInstallBidderAddsKvmAndTheDelegationDropInBeforeEnablingLinger(t *testing.T) {
	sys, root := newFakeHost(t, true)
	if _, err := Install(ctx(), opts(t, root, hostcontract.RoleBidder, "alice.example.com"), sys); err != nil {
		t.Fatal(err)
	}
	m := sys.mutations
	t.Logf("mutations in order: %s", strings.Join(m, " | "))
	linger := indexOf(t, m, "enable-linger")
	for _, before := range []string{"useradd", "gpasswd -a swc-bidder kvm", "system daemon-reload", "apparmor_parser -r"} {
		if indexOf(t, m, before) > linger {
			t.Errorf("%s ran after enable-linger: %v", before, m)
		}
	}
	if indexOf(t, m, "enable --now") < linger {
		t.Errorf("units were enabled before linger: %v", m)
	}
	_, groups, errText := hostcontract.ManagerGroups(root, fakeUID)
	if errText != "" {
		t.Fatal(errText)
	}
	found := false
	for _, g := range groups {
		found = found || g == 36
	}
	if !found {
		t.Fatalf("the user manager started without kvm: groups %v", groups)
	}
}

func TestInstallRefusesWhenTheUserManagerAlreadyRunsWithoutKvm(t *testing.T) {
	sys, root := newFakeHost(t, false)
	if err := sys.CreateAccount(AccountBidder, "/var/lib/socialweb-computer/"+AccountBidder); err != nil {
		t.Fatal(err)
	}
	a, _, _ := sys.LookupAccount(AccountBidder)
	if err := sys.SetLinger(ctx(), a, true); err != nil {
		t.Fatal(err)
	}
	o := opts(t, root, hostcontract.RoleBidder, "alice.example.com")
	o.AdoptAccount = true
	before := len(sys.mutations)
	_, err := Install(ctx(), o, sys)
	if !errors.Is(err, ErrManagerWithoutKVM) || !strings.Contains(err.Error(), "restart user@990") ||
		!strings.Contains(err.Error(), "stops every VM") {
		t.Fatalf("install over a running manager without kvm: %v", err)
	}
	for _, m := range sys.mutations[before:] {
		if strings.Contains(m, "user@") || strings.HasPrefix(m, "disable-linger") || strings.HasPrefix(m, "enable --now") {
			t.Errorf("install restarted or touched the running manager: %s", m)
		}
	}
}

func TestTheSubuidRangeIsAboveEveryExistingEntryOfBothFiles(t *testing.T) {
	sys, root := newFakeHost(t, false)
	if _, err := Install(ctx(), opts(t, root, hostcontract.RoleBidder, "alice.example.com"), sys); err != nil {
		t.Fatal(err)
	}
	sub, gid := sys.read("etc/subuid"), sys.read("etc/subgid")
	mine, ok := hostcontract.AccountRange(sub, AccountBidder, fakeUID)
	mineG, okG := hostcontract.AccountRange(gid, AccountBidder, fakeUID)
	if !ok || !okG || mine != (hostcontract.SubidEntry{Owner: mine.Owner, Start: mineG.Start, Count: mineG.Count, Line: mine.Line}) {
		t.Fatalf("ranges %+v %+v", mine, mineG)
	}
	if mine.Count != DefaultVMSlots*hostcontract.VMSlotIDs || mine.Start%hostcontract.VMSlotIDs != 0 {
		t.Fatalf("range %+v: want %d slots of %d, aligned", mine, DefaultVMSlots, hostcontract.VMSlotIDs)
	}
	for _, content := range []string{baseSubuid, baseSubgid} {
		for _, e := range hostcontract.ParseSubids(content) {
			if mine.Start < e.End() {
				t.Errorf("range %+v is not above %+v", mine, e)
			}
		}
	}
	if p := hostcontract.SubidProblems(sub, gid, AccountBidder, fakeUID); len(p) > 0 {
		t.Fatalf("the contract refuses the range install wrote: %v", p)
	}
	if !strings.HasPrefix(sub, baseSubuid) || !strings.HasPrefix(gid, baseSubgid) {
		t.Fatal("install rewrote the existing entries instead of appending")
	}
}

func TestInstallRefusesTheProcBackend(t *testing.T) {
	sys, root := newFakeHost(t, false)
	o := opts(t, root, hostcontract.RoleGateway)
	o.HostBackend = string(hostsvc.BackendProc)
	if _, err := Install(ctx(), o, sys); !errors.Is(err, ErrProcBackend) {
		t.Fatalf("install with host-backend proc: %v", err)
	}
	if len(sys.mutations) != 0 {
		t.Fatalf("a refused install ran %v", sys.mutations)
	}
}

func TestInstallWritesSystemdUserAsTheHostBackend(t *testing.T) {
	sys, root := newFakeHost(t, false)
	if _, err := Install(ctx(), opts(t, root, hostcontract.RoleGateway), sys); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "var/lib/socialweb-computer/socialweb/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["host-backend"] != string(hostsvc.BackendSystemdUser) || cfg["state-dir"] != "/var/lib/socialweb-computer/socialweb" {
		t.Fatalf("config.json %s", b)
	}
	unit, err := os.ReadFile(filepath.Join(root, "var/lib/socialweb-computer/socialweb/.config/systemd/user/socialweb-computer-gateway.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "CONFIG_PATH_SOCIALWEB_COMPUTER=/var/lib/socialweb-computer/socialweb/config.json") ||
		!strings.Contains(string(unit), "ExecStart=/var/lib/socialweb-computer/socialweb/bin/current/socialweb-computer serve gateway kcp") {
		t.Fatalf("gateway unit:\n%s", unit)
	}
}

func TestInstallRefusesAnExistingAccountItDidNotCreate(t *testing.T) {
	sys, root := newFakeHost(t, false)
	o := opts(t, root, hostcontract.RoleGateway)
	o.Account = "alice"
	_, err := Install(ctx(), o, sys)
	if !errors.Is(err, ErrForeignAccount) || !strings.Contains(err.Error(), "--adopt-account") {
		t.Fatalf("install over alice: %v", err)
	}
	if len(sys.mutations) != 0 {
		t.Fatalf("a refused install ran %v", sys.mutations)
	}
}

func TestInstallDryRunChangesNothing(t *testing.T) {
	sys, root := newFakeHost(t, true)
	pre := snapshot(t, root)
	o := opts(t, root, hostcontract.RoleBidder, "alice.example.com")
	o.DryRun = true
	out := &bytes.Buffer{}
	o.Out = out
	r, err := Install(ctx(), o, sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(sys.mutations) != 0 || sys.checks != 0 {
		t.Fatalf("dry run ran %v", sys.mutations)
	}
	post := snapshot(t, root)
	for k, v := range pre {
		if post[k] != v {
			t.Errorf("dry run changed %s", k)
		}
	}
	if len(r.Changes) == 0 || !strings.Contains(out.String(), "would") {
		t.Fatalf("dry run printed no step list:\n%s", out.String())
	}
}

func TestInstallStopsAtAFailingHostCheckAndARerunCompletesFromThere(t *testing.T) {
	sys, root := newFakeHost(t, false)
	sys.checkFails = "dev-kvm"
	o := opts(t, root, hostcontract.RoleBidder, "alice.example.com")
	if _, err := Install(ctx(), o, sys); err == nil || !strings.Contains(err.Error(), "dev-kvm") {
		t.Fatalf("install with a failing check-host: %v", err)
	}
	sys.checkFails = ""
	before := len(sys.mutations)
	if _, err := Install(ctx(), o, sys); err != nil {
		t.Fatal(err)
	}
	if len(sys.mutations) != before {
		t.Fatalf("the re-run after the fix changed %v", sys.mutations[before:])
	}
}
