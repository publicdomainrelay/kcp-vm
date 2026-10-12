package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap/table"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func testLayout(t *testing.T) devinstance.Layout {
	l, err := devinstance.NewLayout("proof-a", "/s", "/run/user/1000/swc")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func layerParams(t *testing.T) []Params {
	l := testLayout(t)
	devCfg := Config{Layout: l, KcpPath: "/opt/kcp", KinePath: "/opt/kine", PostgresBin: "/opt/pg/bin", Exe: "/opt/swc"}
	var out []Params
	for _, p := range []string{"quick", "prodlike"} {
		apps, err := DevApps(p, l)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, devCfg.Params(apps[0]))
	}
	out = append(out, InstallParams(table.Gateway(), "/var/lib/socialweb-computer"),
		InstallParams(table.Bidder("alice.example.com", "0123456789"), "/var/lib/socialweb-computer"))
	return out
}

func tableUnits(t *testing.T, a table.App) []string {
	tbl, err := table.Table(a)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range tbl {
		out = append(out, e.Unit)
	}
	return out
}

func TestTheBootstrapTableIsTheOnlySourceOfBootstrapUnitsOnBothBackends(t *testing.T) {
	for _, p := range layerParams(t) {
		want := tableUnits(t, p.App)
		specs, err := ProcSpecs(p, "https://127.0.0.1:45000")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range specs {
			got = append(got, s.Name)
		}
		if p.App.DevCore && !slices.Equal(got, want) {
			t.Errorf("%s: proc specs %v, table %v", p.App.Key, got, want)
		}
		files, err := Installed(p, "")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range files {
			names = append(names, f.Unit)
		}
		if !slices.Equal(names, want) {
			t.Errorf("%s: installed units %v, table %v", p.App.Key, names, want)
		}
		for _, f := range files[:len(files)-1] {
			spec, err := p.Spec(f.Entry, "")
			if err != nil {
				t.Fatal(err)
			}
			args, err := hostsvc.ParseArgsEnv(f.File.Env)
			if err != nil || !slices.Equal(args, spec.Exec[1:]) {
				t.Errorf("%s: the installed arguments %v differ from the proc spec's %v (%v)", f.Unit, args, spec.Exec[1:], err)
			}
		}
	}
	unitName := regexp.MustCompile(`"socialweb-(kcp|core)-"`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == "third_party" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "internal/bootstrap/") || rel == "cmd/socialweb-computer/units.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if unitName.Match(b) {
			t.Errorf("%s builds a bootstrap unit name; internal/bootstrap/table is the one source", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEveryRenderedBootstrapUnitFileVerifies(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not on PATH: the rendered unit files are not verified on this host")
	}
	for _, p := range layerParams(t)[2:] {
		state := t.TempDir()
		p = InstallParams(p.App, state)
		files, err := Installed(p, "")
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		var paths []string
		for _, f := range files {
			if f.Entry.Kind == table.KindCore {
				unit := "[Unit]\nDescription=the core drop-in's directives\n" + strings.TrimPrefix(string(f.File.Unit), "[Unit]\n") +
					"\n[Service]\nExecStart=/bin/true\n"
				path := filepath.Join(dir, "socialweb-dropin-check.service")
				os.WriteFile(path, []byte(unit), 0o644)
				paths = append(paths, path)
				continue
			}
			for _, exe := range []string{f.Exec} {
				os.MkdirAll(filepath.Dir(exe), 0o755)
				os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
			}
			path := filepath.Join(dir, f.Unit)
			if err := os.WriteFile(path, f.File.Unit, 0o644); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, path)
		}
		out, err := exec.Command(analyze, append([]string{"--user", "verify"}, paths...)...).CombinedOutput()
		if err != nil || len(strings.TrimSpace(string(out))) > 0 {
			t.Errorf("%s: systemd-analyze --user verify: %v\n%s", p.App.Key, err, out)
		}
	}
}

func TestTheBidderBootstrapHasNoPostgresAndOneSlot(t *testing.T) {
	tbl, err := table.Table(table.Bidder("alice.example.com", "0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[table.Kind]int{}
	for _, e := range tbl {
		kinds[e.Kind]++
	}
	if kinds[table.KindStore] != 0 || kinds[table.KindSlot] != 1 || kinds[table.KindKine] != 1 || kinds[table.KindFront] != 1 || kinds[table.KindCore] != 1 {
		t.Fatalf("bidder table %+v", tbl)
	}
	want := []string{"socialweb-kcp-0123456789-kine@a.service", "socialweb-kcp-0123456789-front.service",
		"socialweb-kcp-0123456789@a.service", "socialweb-computer-bidder@alice.example.com.service"}
	if got := tableUnits(t, table.Bidder("alice.example.com", "0123456789")); !slices.Equal(got, want) {
		t.Fatalf("bidder units %v, want %v", got, want)
	}
	for _, bad := range []table.App{
		{Role: table.RoleBidder, Key: "0123456789", Handle: "a.example.com", Store: table.StorePostgres, Slots: []string{"a"}},
		{Role: table.RoleBidder, Key: "0123456789", Handle: "a.example.com", Store: table.StoreSqlite, Slots: []string{"a", "b"}},
	} {
		if _, err := table.Table(bad); err == nil {
			t.Errorf("a bidder with %s and slots %v was accepted", bad.Store, bad.Slots)
		}
	}
	p := InstallParams(table.Bidder("alice.example.com", "0123456789"), "/var/lib/socialweb-computer")
	specs, err := ProcSpecs(p, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		if strings.Contains(strings.Join(s.Exec, " "), "postgres") {
			t.Errorf("%s names postgres: %v", s.Name, s.Exec)
		}
	}
}

func TestTheBootstrapOrderIsStoreKineFrontSlotCore(t *testing.T) {
	rank := map[table.Kind]int{}
	for i, k := range table.Order() {
		rank[k] = i
	}
	gw := tableUnits(t, table.Gateway())
	want := []string{"socialweb-kcp-gw-pg.service", "socialweb-kcp-gw-kine@a.service", "socialweb-kcp-gw-kine@b.service",
		"socialweb-kcp-gw-front.service", "socialweb-kcp-gw@a.service", "socialweb-kcp-gw@b.service", "socialweb-computer-gateway.service"}
	if !slices.Equal(gw, want) {
		t.Fatalf("gateway units %v, want %v", gw, want)
	}
	for _, p := range layerParams(t) {
		tbl, _ := table.Table(p.App)
		for i := 1; i < len(tbl); i++ {
			if rank[tbl[i].Kind] < rank[tbl[i-1].Kind] {
				t.Errorf("%s: %s before %s", p.App.Key, tbl[i-1].Unit, tbl[i].Unit)
			}
		}
		core := tbl[len(tbl)-1]
		for _, e := range tbl[:len(tbl)-1] {
			if !slices.Contains(core.After, e.Unit) {
				t.Errorf("%s: the core is not After= %s", p.App.Key, e.Unit)
			}
		}
		for _, e := range tbl {
			if e.Kind == table.KindSlot && (!slices.Contains(e.After, p.App.Names().Front()) || len(e.Wants) != 1) {
				t.Errorf("%s: slot %s must start after the front and want its kine: %+v", p.App.Key, e.Unit, e)
			}
		}
	}
	l := testLayout(t)
	for _, p := range []string{"quick", "prodlike"} {
		apps, _ := DevApps(p, l)
		if tableUnits(t, apps[0])[len(tableUnits(t, apps[0]))-1] != l.CoreUnit() {
			t.Errorf("%s: the dev core is not the last unit", p)
		}
	}
}

func TestNoTwoApplicationInstancesAreGivenOneKcp(t *testing.T) {
	l := testLayout(t)
	apps, err := DevApps("bed", l)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]bool{}
	for _, a := range apps {
		roles[a.Role] = true
	}
	if !roles[table.RoleGateway] || !roles[table.RoleBidder] {
		t.Fatalf("the bed composes a gateway and a bidder: %+v", apps)
	}
	cfg := Config{Layout: l, KcpPath: "/opt/kcp", KinePath: "/opt/kine", PostgresBin: "/opt/pg/bin", Exe: "/opt/swc"}
	seen := map[string]string{}
	for _, a := range apps {
		p := cfg.Params(a)
		specs, err := ProcSpecs(p, "")
		if err != nil {
			t.Fatal(err)
		}
		vals := map[string]string{"dir": p.Dir, "front control": p.FrontControl(), "kine socket": p.KineSocket(a.KineOf(a.Slots[0]))}
		for _, s := range specs {
			vals["unit "+s.Name] = s.Name
		}
		for k, v := range vals {
			if prev, dup := seen[v]; dup {
				t.Fatalf("%s %s %s is shared with %s", a.Key, k, v, prev)
			}
			seen[v] = a.Key + " " + k
		}
	}
	for _, p := range []string{"quick", "prodlike"} {
		apps, err := DevApps(p, l)
		if err != nil || len(apps) != 1 {
			t.Fatalf("%s composes %d applications (%v); one kcp per dev instance", p, len(apps), err)
		}
	}
	quick, _ := DevApps("quick", l)
	prodlike, _ := DevApps("prodlike", l)
	if quick[0].Store != table.StoreSqlite || prodlike[0].Store != table.StorePostgres {
		t.Fatalf("quick %s prodlike %s", quick[0].Store, prodlike[0].Store)
	}
}
