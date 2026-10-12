package devcheck

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeEnv map[string]string

func (f fakeEnv) LookupEnv(k string) (string, bool) {
	v, ok := f[k]
	return v, ok
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func byName(rs []Result) map[string]Result {
	out := map[string]Result{}
	for _, r := range rs {
		out[r.Name] = r
	}
	return out
}

func readyHost(t *testing.T) Host {
	root := t.TempDir()
	write(t, root, "dev/kvm", "")
	write(t, root, "dev/net/tun", "")
	write(t, root, "proc/sys/user/max_user_namespaces", "63000\n")
	write(t, root, "etc/subuid", "dev:100000:65536\n")
	write(t, root, "etc/subgid", "dev:100000:65536\n")
	write(t, root, "etc/systemd/logind.conf", "[Login]\n#KillUserProcesses=no\n")
	return Host{Root: root, Euid: 1000, User: "dev", Env: fakeEnv{"XDG_RUNTIME_DIR": "/run/user/1000"},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil }}
}

func TestAReadyHostPassesEveryCheck(t *testing.T) {
	rs := Run(readyHost(t))
	if len(rs) != len(Names) {
		t.Fatalf("%d results for %d checks", len(rs), len(Names))
	}
	for _, r := range rs {
		if r.Status != StatusOK {
			t.Errorf("%s: %s %s", r.Name, r.Status, r.Detail)
		}
	}
	if Failed(rs) {
		t.Fatal("a ready host failed")
	}
}

func TestEveryMissingPrerequisiteIsReportedWithAFixAndNeverFixed(t *testing.T) {
	h := readyHost(t)
	h.Euid = 0
	h.Env = fakeEnv{}
	h.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	os.Remove(filepath.Join(h.Root, "dev/kvm"))
	os.Remove(filepath.Join(h.Root, "dev/net/tun"))
	write(t, h.Root, "proc/sys/user/max_user_namespaces", "0\n")
	write(t, h.Root, "proc/sys/kernel/apparmor_restrict_unprivileged_userns", "1\n")
	write(t, h.Root, "etc/subuid", "someone:100000:65536\n")
	write(t, h.Root, "etc/systemd/logind.conf.d/50-kill.conf", "[Login]\nKillUserProcesses=yes\n")
	rs := byName(Run(h))
	for _, name := range []string{CheckNotRoot, CheckRuntimeDir, CheckKVM, CheckTun, CheckUserns, CheckNewuidmap, CheckSubuid} {
		if rs[name].Status != StatusFail || rs[name].Fix == "" {
			t.Errorf("%s: %+v, want fail with a fix", name, rs[name])
		}
	}
	for _, name := range []string{CheckAppArmorUserns, CheckLogindKill} {
		if rs[name].Status != StatusWarn || rs[name].Fix == "" {
			t.Errorf("%s: %+v, want warn with a fix", name, rs[name])
		}
	}
	if _, err := os.Stat(filepath.Join(h.Root, "dev/kvm")); !os.IsNotExist(err) {
		t.Fatalf("dev check created /dev/kvm")
	}
	if !Failed(Run(h)) {
		t.Fatal("not failed")
	}
}
