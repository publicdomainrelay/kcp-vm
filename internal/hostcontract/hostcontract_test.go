package hostcontract

import (
	"bytes"
	"strings"
	"testing"
)

func goodSent() map[string]any {
	spec, extra := ProbeSpec(RoleGateway, "t", []string{"/usr/bin/true"}, "/state/probe/t.log")
	sent := map[string]any{
		"Description": spec.Description, "ExecStart": [][]any{{"/usr/bin/true"}}, "Type": "exec",
		"CollectMode": "inactive", "KillMode": "control-group", "OOMPolicy": "kill", "Slice": "socialweb.slice",
		"Restart": "no", "KillSignal": int32(15), "TimeoutStopUSec": uint64(5000000), "MemoryMax": uint64(64 << 20),
		"MemorySwapMax": uint64(0), "TasksMax": uint64(16), "CPUWeight": uint64(50), "CPUQuotaPerSecUSec": uint64(500000),
		"Environment": []string{"SOCIALWEB_PROBE=1"}, "StandardOutputFileToAppend": "/state/probe/t.log",
		"StandardErrorFileToAppend": "/state/probe/t.log", "After": []string{"basic.target"}, "Wants": []string{"basic.target"},
	}
	for k, v := range extra {
		sent[k] = v
	}
	return sent
}

func readBackOf(sent map[string]any) map[string]any {
	back := map[string]any{}
	for k, v := range sent {
		switch k {
		case "StandardOutputFileToAppend":
			back["StandardOutput"] = "append"
		case "StandardErrorFileToAppend":
			back["StandardError"] = "append"
		case "ExecStart":
			back[k] = [][]any{{"/usr/bin/true", []string{"/usr/bin/true"}, false}}
		case "After":
			back[k] = []string{"basic.target", "-.mount", "socialweb.slice"}
		default:
			back[k] = v
		}
	}
	return back
}

const kvmGid = 36

func goodFacts(role Role) Facts {
	sent := goodSent()
	if role == RoleBidder {
		delete(sent, "FileDescriptorStoreMax")
	}
	return Facts{
		Role: role, Account: "swc-bidder", UID: 990, Euid: 990, StateDir: "/var/lib/socialweb-computer/swc-bidder",
		SystemdVersion: "256.7-1", ProbeSent: sent, ProbeReadBack: readBackOf(sent), CgroupV2: true,
		ManagerControllers: []string{"cpu", "memory", "pids"}, Linger: true, RuntimeDir: "/run/user/990",
		BootID: "b0", KVMGid: kvmGid, AccountInKVM: true, ManagerPID: 1234, ManagerGroups: []int{990, kvmGid},
		PastaChecked: true, Subuid: "alice:100000:65536\nswc-bidder:165536:16777216\n",
		Subgid: "alice:100000:65536\nswc-bidder:165536:16777216\n",
		Helpers: []Helper{{Name: "newuidmap", Path: "/usr/bin/newuidmap", Caps: true},
			{Name: "newgidmap", Path: "/usr/bin/newgidmap", Setuid: true}},
		UIDMapChecked: true, ACLChecked: true, Tzdata: true,
	}
}

func failedOnly(t *testing.T, f Facts) []Result {
	t.Helper()
	var out []Result
	for _, r := range Evaluate(f) {
		if r.Status == StatusFail {
			out = append(out, r)
		}
	}
	return out
}

func TestAGoodHostPassesEveryCheckForBothRoles(t *testing.T) {
	for _, role := range []Role{RoleGateway, RoleBidder} {
		if bad := failedOnly(t, goodFacts(role)); len(bad) > 0 {
			t.Errorf("%s: a good host failed %+v", role, bad)
		}
	}
}

func TestASystemdOlderThan253IsRefused(t *testing.T) {
	f := goodFacts(RoleBidder)
	f.SystemdVersion = "252.33-1~deb12u1"
	bad := failedOnly(t, f)
	if len(bad) != 1 || bad[0].Name != CheckSystemdVersion || !strings.Contains(bad[0].Detail, "253") {
		t.Fatalf("systemd 252: failures %+v, want exactly %s naming 253", bad, CheckSystemdVersion)
	}
}

func TestEachRuntimePropertyIsReadBackFromAProbeUnit(t *testing.T) {
	sent := goodSent()
	for name := range sent {
		f := goodFacts(RoleGateway)
		readName := name
		if rn, ok := readBackName[name]; ok {
			readName = rn
		}
		delete(f.ProbeReadBack, readName)
		bad := failedOnly(t, f)
		found := false
		for _, r := range bad {
			if (r.Name == CheckProbeProperties || r.Name == CheckFDStore) && strings.Contains(r.Detail, name) || r.Name == CheckFDStore && name == "FileDescriptorStoreMax" {
				found = true
			}
		}
		if !found {
			t.Errorf("dropping %s from the read-back: failures %+v, want %s naming it", name, bad, CheckProbeProperties)
		}
	}
	f := goodFacts(RoleGateway)
	f.ProbeReadBack["MemorySwapMax"] = uint64(1 << 20)
	if bad := failedOnly(t, f); len(bad) != 1 || !strings.Contains(bad[0].Detail, "MemorySwapMax") {
		t.Errorf("a changed MemorySwapMax: failures %+v", bad)
	}
}

func TestAManagerWithoutKvmInItsGroupsIsRefusedNamingTheRestart(t *testing.T) {
	f := goodFacts(RoleBidder)
	f.ManagerGroups = []int{990}
	bad := failedOnly(t, f)
	if len(bad) != 1 || bad[0].Name != CheckManagerKVM {
		t.Fatalf("failures %+v, want exactly %s", bad, CheckManagerKVM)
	}
	if !strings.Contains(bad[0].Fix, "restart user@990") || !strings.Contains(bad[0].Fix, "stops every VM") ||
		!strings.Contains(bad[0].Fix, "reboot") {
		t.Errorf("fix %q does not name the user@ restart, that it stops every VM, and reboot", bad[0].Fix)
	}
	if !strings.Contains(bad[0].Detail, "in kvm in /etc/group") {
		t.Errorf("detail %q does not say the account is already in kvm", bad[0].Detail)
	}
}

func TestNoFixAdvisesChmodOfDevKvmOrLoggingInAgainAsTheRemedy(t *testing.T) {
	for _, mutate := range []func(*Facts){
		func(f *Facts) { f.KVMErr = "permission denied" },
		func(f *Facts) { f.KVMMissing, f.KVMErr = true, "no such file" },
		func(f *Facts) { f.ManagerGroups = nil },
		func(f *Facts) { f.ManagerGroups, f.AccountInKVM = nil, false },
	} {
		f := goodFacts(RoleBidder)
		mutate(&f)
		for _, r := range failedOnly(t, f) {
			fix := strings.ToLower(r.Fix)
			if strings.Contains(fix, "chmod") && !strings.Contains(fix, "never chmod") {
				t.Errorf("%s fix advises chmod: %q", r.Name, r.Fix)
			}
			if strings.Contains(fix, "log in again") && !strings.Contains(fix, "does not refresh") {
				t.Errorf("%s fix advises logging in again: %q", r.Name, r.Fix)
			}
		}
	}
}

func TestASubuidRangeOverlappingAnotherEntryIsRefused(t *testing.T) {
	f := goodFacts(RoleBidder)
	f.Subuid += "podman:200000:65536\n"
	bad := failedOnly(t, f)
	if len(bad) != 1 || bad[0].Name != CheckSubids || !strings.Contains(bad[0].Detail, "overlaps podman") {
		t.Fatalf("an overlapping podman range: failures %+v", bad)
	}
	f = goodFacts(RoleBidder)
	f.Subgid += "lxd:1000000:65536\n"
	if bad := failedOnly(t, f); len(bad) != 1 || !strings.Contains(bad[0].Detail, "overlaps lxd") {
		t.Fatalf("an overlapping lxd subgid range: failures %+v", bad)
	}
}

func TestAMissingSubgidRangeIsRefusedEvenWithASubuidRange(t *testing.T) {
	f := goodFacts(RoleBidder)
	f.Subgid = "alice:100000:65536\n"
	bad := failedOnly(t, f)
	if len(bad) != 1 || bad[0].Name != CheckSubids || !strings.Contains(bad[0].Detail, "/etc/subgid") {
		t.Fatalf("no subgid range: failures %+v", bad)
	}
	if strings.Contains(bad[0].Detail, "/etc/subuid") {
		t.Errorf("detail blames /etc/subuid too: %q", bad[0].Detail)
	}
}

func TestEachMissingPrerequisiteIsRefusedByName(t *testing.T) {
	cases := []struct {
		role   Role
		check  string
		mutate func(*Facts)
	}{
		{RoleBidder, CheckAccount, func(f *Facts) { f.Euid = 0 }},
		{RoleBidder, CheckSystemdVersion, func(f *Facts) { f.SystemdVersion, f.VersionErr = "", "connection refused" }},
		{RoleBidder, CheckProbeProperties, func(f *Facts) { f.ProbeErr = "Unknown assignment" }},
		{RoleBidder, CheckCgroupV2, func(f *Facts) { f.CgroupV2 = false }},
		{RoleBidder, CheckDelegation, func(f *Facts) { f.ManagerControllers = []string{"memory", "pids"} }},
		{RoleBidder, CheckLinger, func(f *Facts) { f.Linger = false }},
		{RoleBidder, CheckSocketPath, func(f *Facts) { f.RuntimeDir = "/run/" + strings.Repeat("x", 40) }},
		{RoleBidder, CheckBootID, func(f *Facts) { f.BootID = "" }},
		{RoleBidder, CheckKVM, func(f *Facts) { f.KVMErr = "permission denied" }},
		{RoleBidder, CheckManagerKVM, func(f *Facts) { f.ManagerGroups = []int{990} }},
		{RoleBidder, CheckTun, func(f *Facts) { f.TunErr = "no such device" }},
		{RoleBidder, CheckPastaUserns, func(f *Facts) { f.PastaErr = "exit status 1: Couldn't create user namespace" }},
		{RoleBidder, CheckAppArmor, func(f *Facts) { f.AppArmorRestricts = true }},
		{RoleBidder, CheckSubids, func(f *Facts) { f.Subuid = "" }},
		{RoleBidder, CheckUIDMapHelpers, func(f *Facts) { f.Helpers[0].Caps = false }},
		{RoleBidder, CheckUIDMapNamespace, func(f *Facts) { f.UIDMapErr = "newuidmap: write to uid_map failed" }},
		{RoleBidder, CheckStateACL, func(f *Facts) { f.ACLErr = "the filesystem does not support POSIX ACLs" }},
		{RoleGateway, CheckFDStore, func(f *Facts) {
			delete(f.ProbeReadBack, "FileDescriptorStoreMax")
			delete(f.ProbeSent, "FileDescriptorStoreMax")
		}},
		{RoleGateway, CheckTzdata, func(f *Facts) { f.Tzdata = false }},
	}
	for _, c := range cases {
		f := goodFacts(c.role)
		c.mutate(&f)
		bad := failedOnly(t, f)
		if len(bad) == 0 || bad[0].Name != c.check {
			t.Errorf("%s: failures %+v, want %s first", c.check, bad, c.check)
			continue
		}
		if bad[0].Fix == "" {
			t.Errorf("%s fails with no fix instruction", c.check)
		}
		var buf bytes.Buffer
		Write(&buf, Evaluate(f), false)
		if !strings.Contains(buf.String(), "FAIL  "+c.check) {
			t.Errorf("%s: the report does not name it:\n%s", c.check, buf.String())
		}
	}
}

func TestTheProbeAsksForEveryPropertyTheRuntimeSets(t *testing.T) {
	spec, _ := ProbeSpec(RoleBidder, "x", []string{"/bin/true"}, "/s/p.log")
	if !strings.HasPrefix(spec.Name, ProbePrefix) || spec.Limits.MemoryMax == 0 || spec.Limits.TasksMax == 0 ||
		spec.Limits.CPUQuotaPercent == 0 || spec.Limits.CPUWeight == 0 || spec.OOMPolicy == "" || spec.KillSignal == 0 ||
		spec.StopTimeout == 0 || len(spec.After) == 0 || len(spec.Wants) == 0 || spec.Slice == "" ||
		spec.Output.StdoutFile == "" {
		t.Fatalf("probe spec leaves a runtime property unset: %+v", spec)
	}
}

func TestGroupsAreReadFromTheManagersOwnStatus(t *testing.T) {
	got := ParseGroups("Name:\tsystemd\nUid:\t990\nGroups:\t36 990 \nNSpid:\t1\n")
	if len(got) != 2 || got[0] != 36 || got[1] != 990 {
		t.Fatalf("groups %v", got)
	}
	gid, member := GroupMembership("wheel:x:10:alice\nkvm:x:36:qemu,swc-bidder\n", "kvm", "swc-bidder")
	if gid != 36 || !member {
		t.Fatalf("kvm membership %d %v", gid, member)
	}
	if !AppArmorProfileLoaded("foo (enforce)\nsocialweb-pasta (complain)\n", AppArmorProfileName) {
		t.Fatal("a loaded profile line was not recognised")
	}
}
