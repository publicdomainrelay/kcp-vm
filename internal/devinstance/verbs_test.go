package devinstance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

func TestTheStateOfAnInstanceLivesUnderDevSlashItsID(t *testing.T) {
	l, err := NewLayout("proof-a", "/home/u/.local/state/socialweb-computer", "/run/user/1000/swc")
	if err != nil {
		t.Fatal(err)
	}
	if l.StateDir != "/home/u/.local/state/socialweb-computer/dev/proof-a" {
		t.Fatalf("state dir %s", l.StateDir)
	}
	if DevRoot("/home/u/.local/state/socialweb-computer") != filepath.Dir(l.StateDir) {
		t.Fatalf("DevRoot %s is not the parent of %s", DevRoot("/home/u/.local/state/socialweb-computer"), l.StateDir)
	}
}

func TestATaskTreeWorktreeIsNamedAfterItsTask(t *testing.T) {
	base := t.TempDir()
	taskRoot := filepath.Join(base, "tasks", "dev-instance")
	wt := worktree(t, taskRoot, "socialweb-computer-kcp")
	env := mapEnv{"TASK_NAME": "dev-instance", "TASK_ROOT": taskRoot}
	id, err := ID("", filepath.Join(wt, "sub"), env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "dev-instance-") || len(id) != len("dev-instance-")+8 {
		t.Fatalf("id %q, want dev-instance-<hash8>", id)
	}
	other := worktree(t, filepath.Join(base, "elsewhere"), "socialweb-computer-kcp")
	oid, err := ID("", other, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(oid, "socialweb-comput-") {
		t.Fatalf("a worktree outside TASK_ROOT took the task's name: %q", oid)
	}
	sib := worktree(t, taskRoot, "atproto-market")
	sid, _ := ID("", sib, env)
	if sid == id {
		t.Fatalf("two worktrees of one task share id %s", id)
	}
}

func TestTheLeaseFileIsPerUserNotPerTask(t *testing.T) {
	cases := []struct {
		env  mapEnv
		want string
	}{
		{mapEnv{"TASK_STATE_DIR": "/t", "XDG_STATE_HOME": "/s", "HOME": "/h"}, "/s/socialweb-computer/ports/leases.json"},
		{mapEnv{"TASK_STATE_DIR": "/t", "HOME": "/h"}, "/h/.local/state/socialweb-computer/ports/leases.json"},
	}
	for _, c := range cases {
		got, err := LeasePath(c.env)
		if err != nil || got != c.want {
			t.Errorf("%v: %q %v, want %q", c.env, got, err, c.want)
		}
	}
	if _, err := LeasePath(mapEnv{}); err == nil {
		t.Errorf("no HOME and no XDG_STATE_HOME: no error")
	}
}

func TestTwoDevInstancesLeaseDistinctPortsFromThePerUserFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports", "leases.json")
	a, err := OpenLeases(path, "wt-a-11111111")
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenLeases(path, "wt-b-22222222")
	if err != nil {
		t.Fatal(err)
	}
	la, err := a.Allocate(portallocator.Request{Cluster: "c", Purpose: portallocator.PurposeApiserverLink})
	if err != nil {
		t.Fatal(err)
	}
	lb, err := b.Allocate(portallocator.Request{Cluster: "c", Purpose: portallocator.PurposeApiserverLink})
	if err != nil {
		t.Fatal(err)
	}
	if la.Port == lb.Port || la.Port < portallocator.DevFirst || lb.Port > portallocator.DevLast {
		t.Fatalf("ports %d %d", la.Port, lb.Port)
	}
}

func TestTheWorktreeMarkerPointsAtTheStateAndOnlyItsOwnerRemovesIt(t *testing.T) {
	wt := worktree(t, t.TempDir(), "wt")
	state := filepath.Join(t.TempDir(), "dev", "wt-1")
	other := filepath.Join(t.TempDir(), "dev", "wt-2")
	if err := LinkMarker(wt, state); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(wt, Marker)); got != state {
		t.Fatalf("marker -> %q", got)
	}
	if err := LinkMarker(wt, state); err != nil {
		t.Fatalf("relinking the same target: %v", err)
	}
	if UnlinkMarker(wt, other) {
		t.Fatalf("another instance removed the marker")
	}
	if !UnlinkMarker(wt, state) {
		t.Fatalf("the owner did not remove the marker")
	}
	if _, err := os.Lstat(filepath.Join(wt, Marker)); !os.IsNotExist(err) {
		t.Fatalf("marker left: %v", err)
	}
	gi, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gi), "/"+Marker) {
		t.Fatalf(".gitignore does not ignore %s", Marker)
	}
}

func sampleRecord(t *testing.T, root, id string) Record {
	t.Helper()
	l, err := NewLayout(id, root, "/run/user/1000/swc")
	if err != nil {
		t.Fatal(err)
	}
	return Record{Layout: l, Profile: "minimal", BaseWorkspace: "root", AdminKubeconfig: l.AdminKubeconfig(),
		KcpURL: "https://127.0.0.1:40001", KineURL: "http://127.0.0.1:40002", Ready: true,
		Units: []Unit{{Role: "kine-a", Unit: l.KineUnit(), Port: 40002}, {Role: "kcp-a", Unit: l.KcpUnit(), Port: 40001}}}
}

func TestDevEnvExportsWhatAShellOrATSToolNeedsToReachItsKcp(t *testing.T) {
	rec := sampleRecord(t, "/s", "it's-a")
	ex := Exports(rec, []portallocator.Lease{{Port: 15000, Purpose: portallocator.PurposeApiserverLink, Cluster: "c1", AppInstance: "it's-a"}})
	got := map[string]string{}
	for _, e := range ex {
		if _, dup := got[e.Name]; dup {
			t.Fatalf("%s exported twice", e.Name)
		}
		got[e.Name] = e.Value
	}
	want := map[string]string{
		"KUBECONFIG":                            rec.AdminKubeconfig,
		"SOCIALWEB_KCP_KUBECONFIG":              rec.AdminKubeconfig,
		"SOCIALWEB_KCP_HOST":                    "https://127.0.0.1:40001",
		"SOCIALWEB_KCP_BASE_WORKSPACE":          "root",
		"SOCIALWEB_INSTANCE":                    "it's-a",
		"SOCIALWEB_DEV_STATE_DIR":               "/s/dev/it's-a",
		"SOCIALWEB_DEV_PORT_KCP_A":              "40001",
		"SOCIALWEB_DEV_PORT_KINE_A":             "40002",
		"SOCIALWEB_DEV_LEASE_C1_APISERVER_LINK": "15000",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s=%q, want %q", k, got[k], v)
		}
	}
	sh := ShellExports(ex)
	if !strings.Contains(sh, `export SOCIALWEB_INSTANCE='it'\''s-a'`+"\n") {
		t.Fatalf("not shell-quoted:\n%s", sh)
	}
}

func TestDevLsListsEveryInstanceOfTheStateRoot(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"b-inst", "a-inst"} {
		rec := sampleRecord(t, root, id)
		rec.Created = time.Now().UTC()
		if err := WriteJSON(rec.Layout.Record, rec); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(DevRoot(root), "half-made"), 0o700)
	recs, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Layout.ID != "a-inst" || recs[1].Layout.ID != "b-inst" {
		t.Fatalf("listed %+v", recs)
	}
	empty, err := List(filepath.Join(root, "none"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("a missing root: %v %v", empty, err)
	}
}

func TestDevLogsNamesEveryUnitByRoleOrUnitName(t *testing.T) {
	rec := sampleRecord(t, "/s", "inst")
	l := rec.Layout
	cases := map[string]string{
		"kine-a":     filepath.Join(l.Logs, "kine-a.log"),
		l.KineUnit(): filepath.Join(l.Logs, "kine-a.log"),
		"kcp-a":      filepath.Join(l.Logs, "kcp-a.log"),
		l.KcpUnit():  filepath.Join(l.Logs, "kcp-a.log"),
		"core":       filepath.Join(l.Logs, "core.log"),
		l.CoreUnit(): filepath.Join(l.Logs, "core.log"),
		"devsup":     filepath.Join(l.SupState, "devsup.log"),
	}
	for name, want := range cases {
		got, err := LogFile(l, name)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", name, got, err, want)
		}
	}
	if _, err := LogFile(l, "nope"); err == nil || !strings.Contains(err.Error(), "kcp-a") {
		t.Errorf("an unknown unit: %v", err)
	}
	if n := LogNames(l); len(n) != 6 {
		t.Errorf("names %v", n)
	}
}
