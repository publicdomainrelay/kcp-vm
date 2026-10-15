package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const (
	fakeUID        = 990
	fakeManagerPID = 4242
	basePasswd     = "root:x:0:0:root:/root:/bin/bash\nalice:x:1000:1000:Alice:/home/alice:/bin/bash\n"
	baseGroup      = "root:x:0:\nkvm:x:36:qemu\nalice:x:1000:\n"
	baseSubuid     = "alice:100000:65536\npodman:300000:65536\n"
	baseSubgid     = "alice:100000:65536\nlxd:900000:1000000\n"
)

type fakeSystem struct {
	t          *testing.T
	root       string
	mutations  []string
	owners     map[string][2]int
	units      map[string]bool
	enabled    map[string]bool
	checkFails string
	checks     int
	apparmor   bool
}

func newFakeHost(t *testing.T, apparmor bool) (*fakeSystem, string) {
	t.Helper()
	root := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("etc/passwd", basePasswd)
	write("etc/group", baseGroup)
	write("etc/subuid", baseSubuid)
	write("etc/subgid", baseSubgid)
	write("sys/kernel/security/apparmor/profiles", "")
	if apparmor {
		write("proc/sys/kernel/apparmor_restrict_unprivileged_userns", "1\n")
	}
	if err := os.MkdirAll(filepath.Join(root, "var/lib/systemd/linger"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeSystem{t: t, root: root, owners: map[string][2]int{}, units: map[string]bool{}, enabled: map[string]bool{},
		apparmor: apparmor}, root
}

func (f *fakeSystem) path(p string) string { return filepath.Join(f.root, p) }

func (f *fakeSystem) mutate(what string) { f.mutations = append(f.mutations, what) }

func (f *fakeSystem) read(p string) string {
	b, _ := os.ReadFile(f.path(p))
	return string(b)
}

func (f *fakeSystem) write(p, c string) {
	if err := os.MkdirAll(filepath.Dir(f.path(p)), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.path(p), []byte(c), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSystem) LookupAccount(name string) (Account, bool, error) {
	for _, line := range strings.Split(f.read("etc/passwd"), "\n") {
		p := strings.Split(line, ":")
		if len(p) == 7 && p[0] == name {
			uid, _ := strconv.Atoi(p[2])
			gid, _ := strconv.Atoi(p[3])
			return Account{Name: name, UID: uid, GID: gid, Home: p[5]}, true, nil
		}
	}
	return Account{}, false, nil
}

func (f *fakeSystem) CreateAccount(name, home string) error {
	f.mutate("useradd " + name)
	f.write("etc/passwd", f.read("etc/passwd")+fmt.Sprintf("%s:x:%d:%d::%s:/usr/sbin/nologin\n", name, fakeUID, fakeUID, home))
	f.write("etc/group", f.read("etc/group")+fmt.Sprintf("%s:x:%d:\n", name, fakeUID))
	return nil
}

func dropLine(content, prefix string) string {
	var out []string
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "")
}

func (f *fakeSystem) DeleteAccount(name string) error {
	f.mutate("userdel " + name)
	f.write("etc/passwd", dropLine(f.read("etc/passwd"), name+":"))
	f.write("etc/group", dropLine(f.read("etc/group"), name+":"))
	return nil
}

func (f *fakeSystem) editGroup(group string, edit func(members []string) []string) {
	var out []string
	for _, line := range strings.SplitAfter(f.read("etc/group"), "\n") {
		p := strings.Split(strings.TrimSuffix(line, "\n"), ":")
		if len(p) == 4 && p[0] == group {
			var members []string
			if p[3] != "" {
				members = strings.Split(p[3], ",")
			}
			p[3] = strings.Join(edit(members), ",")
			line = strings.Join(p, ":") + "\n"
		}
		out = append(out, line)
	}
	f.write("etc/group", strings.Join(out, ""))
}

func (f *fakeSystem) AddToGroup(account, group string) error {
	f.mutate("gpasswd -a " + account + " " + group)
	f.editGroup(group, func(m []string) []string { return append(m, account) })
	return nil
}

func (f *fakeSystem) RemoveFromGroup(account, group string) error {
	f.mutate("gpasswd -d " + account + " " + group)
	f.editGroup(group, func(m []string) []string {
		var out []string
		for _, x := range m {
			if x != account {
				out = append(out, x)
			}
		}
		return out
	})
	return nil
}

func (f *fakeSystem) Chown(path string, uid, gid int) error {
	f.mutate("chown " + strings.TrimPrefix(path, f.root))
	f.owners[path] = [2]int{uid, gid}
	return nil
}

func (f *fakeSystem) Owner(path string) (int, int, error) {
	if _, err := os.Lstat(path); err != nil {
		return 0, 0, err
	}
	if o, ok := f.owners[path]; ok {
		return o[0], o[1], nil
	}
	return 0, 0, nil
}

func (f *fakeSystem) ReloadSystemManager(ctx context.Context) error {
	f.mutate("system daemon-reload")
	return nil
}

func (f *fakeSystem) LoadAppArmor(path string) error {
	f.mutate("apparmor_parser -r " + strings.TrimPrefix(path, f.root))
	f.write("sys/kernel/security/apparmor/profiles", f.read("sys/kernel/security/apparmor/profiles")+hostcontract.AppArmorProfileName+" (unconfined)\n")
	return nil
}

func (f *fakeSystem) UnloadAppArmor(path string) error {
	f.mutate("apparmor_parser -R " + strings.TrimPrefix(path, f.root))
	f.write("sys/kernel/security/apparmor/profiles", dropLine(f.read("sys/kernel/security/apparmor/profiles"), hostcontract.AppArmorProfileName+" "))
	return nil
}

func (f *fakeSystem) managerDir(uid int) string {
	return fmt.Sprintf("sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/init.scope/cgroup.procs", uid, uid)
}

func (f *fakeSystem) startManager(a Account) {
	var groups []string
	for _, line := range strings.Split(f.read("etc/group"), "\n") {
		p := strings.Split(line, ":")
		if len(p) != 4 {
			continue
		}
		for _, m := range strings.Split(p[3], ",") {
			if m == a.Name {
				groups = append(groups, p[2])
			}
		}
	}
	groups = append(groups, strconv.Itoa(a.GID))
	f.write(f.managerDir(a.UID), strconv.Itoa(fakeManagerPID)+"\n")
	f.write(fmt.Sprintf("proc/%d/status", fakeManagerPID), "Name:\tsystemd\nGroups:\t"+strings.Join(groups, " ")+"\n")
}

func (f *fakeSystem) SetLinger(ctx context.Context, a Account, on bool) error {
	p := "var/lib/systemd/linger/" + a.Name
	if on {
		f.mutate("enable-linger " + a.Name)
		f.write(p, "")
		f.startManager(a)
		return nil
	}
	f.mutate("disable-linger " + a.Name)
	os.Remove(f.path(p))
	os.RemoveAll(f.path(fmt.Sprintf("sys/fs/cgroup/user.slice/user-%d.slice", a.UID)))
	os.RemoveAll(f.path(fmt.Sprintf("proc/%d", fakeManagerPID)))
	f.units = map[string]bool{}
	return nil
}

func (f *fakeSystem) User(ctx context.Context, a Account) (UserManager, error) {
	if _, err := os.Stat(f.path(f.managerDir(a.UID))); err != nil {
		return nil, fmt.Errorf("user@%d is not running", a.UID)
	}
	return &fakeUser{f: f, a: a}, nil
}

func (f *fakeSystem) CheckHost(ctx context.Context, a Account, binary string, role hostcontract.Role, stateDir string) error {
	f.checks++
	if f.checkFails != "" {
		return fmt.Errorf("check-host: the %s host contract is not met: %s", role, f.checkFails)
	}
	return nil
}

type fakeUser struct {
	f *fakeSystem
	a Account
}

func (u *fakeUser) Reload(ctx context.Context) error {
	u.f.mutate("user daemon-reload")
	return nil
}

func (u *fakeUser) Restart(ctx context.Context, units []string) error {
	for _, unit := range units {
		u.f.mutate("user restart " + unit)
	}
	return nil
}

func (u *fakeUser) wants(unit string) string {
	return filepath.Join(u.f.root, u.a.Home, ".config/systemd/user/default.target.wants", unit)
}

func (u *fakeUser) EnableNow(ctx context.Context, units []string) error {
	for _, unit := range units {
		u.f.mutate("enable --now " + unit)
		target := strings.SplitN(unit, "@", 2)[0]
		if strings.Contains(unit, "@") {
			target += "@.service"
		}
		_ = os.MkdirAll(filepath.Dir(u.wants(unit)), 0o755)
		_ = os.Symlink("../"+target, u.wants(unit))
		u.f.units[unit] = true
	}
	return nil
}

func (u *fakeUser) DisableStop(ctx context.Context, units []string) error {
	for _, unit := range units {
		u.f.mutate("disable --now " + unit)
		os.Remove(u.wants(unit))
		delete(u.f.units, unit)
	}
	return nil
}

func (u *fakeUser) List(ctx context.Context) ([]hostsvc.UnitStatus, error) {
	var out []hostsvc.UnitStatus
	for name, active := range u.f.units {
		st := hostsvc.Inactive
		if active {
			st = hostsvc.Active
		}
		out = append(out, hostsvc.UnitStatus{Name: name, State: st})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (u *fakeUser) Stop(ctx context.Context, unit string) error {
	u.f.mutate("stop " + unit)
	delete(u.f.units, unit)
	return nil
}

func (u *fakeUser) Close() {}
