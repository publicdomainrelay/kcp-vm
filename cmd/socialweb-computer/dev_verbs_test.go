package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/testdirs"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/capabilities"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
)

func TestEveryDevVerbOfThisItemIsBuilt(t *testing.T) {
	for _, path := range []string{"dev up", "dev status", "dev down", "dev ls", "dev logs", "dev env", "dev check", "dev restart"} {
		v, _, ok := capabilities.Find(strings.Fields(path))
		if !ok || !v.Built() {
			t.Errorf("%s: not built (%s)", path, v.BuiltBy)
		}
		if _, ok := builtVerbs()[path]; !ok {
			t.Errorf("%s has no run function", path)
		}
	}
}

func TestFlagsMayFollowTheWordsTheyQualify(t *testing.T) {
	spec, err := clicfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := flagsFirst(spec, []string{"core", "--binary", "/b", "--json", "x", "--tail=3", "--", "-y"})
	want := []string{"--binary", "/b", "--json", "--tail=3", "core", "x", "-y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

func devEnv(t *testing.T) clicfg.MapEnv {
	return clicfg.MapEnv{"XDG_RUNTIME_DIR": testdirs.Short(t), "TASK_STATE_DIR": t.TempDir(), "XDG_STATE_HOME": t.TempDir()}
}

func TestTheDevVerbsOfAnInstanceThatIsNotUpSayDevUpFirst(t *testing.T) {
	env := devEnv(t)
	for _, args := range [][]string{{"dev", "env", "--instance", "t1"}, {"dev", "restart", "core", "--instance", "t1"}} {
		code, _, stderr := runT(t, env, "linux", args...)
		if code != 1 || !strings.Contains(stderr, "dev up first") {
			t.Errorf("%v: exit %d %q", args, code, stderr)
		}
	}
	code, _, stderr := runT(t, env, "linux", "dev", "logs", "--instance", "t1")
	if code != 2 || !strings.Contains(stderr, "kcp-a") {
		t.Errorf("dev logs without a unit: exit %d %q", code, stderr)
	}
	code, _, stderr = runT(t, env, "linux", "dev", "restart", "kcp", "--instance", "t1")
	if code != 2 || !strings.Contains(stderr, "M3-KCP-ROLL") {
		t.Errorf("dev restart kcp: exit %d %q", code, stderr)
	}
	code, out, _ := runT(t, env, "linux", "dev", "ls")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("dev ls of an empty root: exit %d %q", code, out)
	}
}

func TestDevEnvPrintsTheInstancesExportsForShellsAndJSON(t *testing.T) {
	env := devEnv(t)
	root, _ := devinstance.StateRoot("", env)
	rt, _ := devinstance.RuntimeRoot(env)
	l, err := devinstance.NewLayout("t1", root, rt)
	if err != nil {
		t.Fatal(err)
	}
	rec := devinstance.Record{Layout: l, Profile: "minimal", BaseWorkspace: "root", AdminKubeconfig: l.AdminKubeconfig(),
		KcpURL: "https://127.0.0.1:41000", Units: []devinstance.Unit{{Role: "kcp-a", Port: 41000}}}
	if err := devinstance.WriteJSON(l.Record, rec); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(l.Logs, 0o700)
	os.WriteFile(filepath.Join(l.Logs, "kcp-a.log"), []byte("one\ntwo\nthree\n"), 0o600)
	code, out, stderr := runT(t, env, "linux", "dev", "env", "--instance", "t1")
	if code != 0 || !strings.Contains(out, "export KUBECONFIG='"+l.AdminKubeconfig()+"'\n") || !strings.Contains(out, "export SOCIALWEB_DEV_PORT_KCP_A='41000'") {
		t.Fatalf("exit %d %q %q", code, out, stderr)
	}
	code, out, _ = runT(t, env, "linux", "dev", "env", "--instance", "t1", "--json")
	var m map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &m) != nil || m["SOCIALWEB_KCP_HOST"] != "https://127.0.0.1:41000" {
		t.Fatalf("json: exit %d %q", code, out)
	}
	code, out, _ = runT(t, env, "linux", "dev", "logs", "kcp-a", "--instance", "t1", "--tail", "2")
	if code != 0 || out != "two\nthree\n" {
		t.Fatalf("dev logs --tail 2: exit %d %q", code, out)
	}
	code, out, _ = runT(t, env, "linux", "dev", "ls")
	var ls []lsEntry
	if code != 0 || json.Unmarshal([]byte(out), &ls) != nil || len(ls) != 1 || ls[0].Instance != "t1" || ls[0].Reachable {
		t.Fatalf("dev ls: exit %d %q", code, out)
	}
}
