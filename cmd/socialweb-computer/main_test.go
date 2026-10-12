package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/capabilities"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

func runT(t *testing.T, env clicfg.MapEnv, goos string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, env, &out, &errb, goos, "amd64")
	return code, out.String(), errb.String()
}

func TestAVerbWithNoBackendOnThisOSExits2NamingItsPlan(t *testing.T) {
	cases := []struct {
		goos string
		args []string
		plan string
	}{
		{"windows", []string{"run", "bidder"}, "plan 0011"},
		{"darwin", []string{"run", "bidder"}, "plan 0012"},
		{"windows", []string{"dev", "up"}, "plan 0011"},
		{"darwin", []string{"internal", "devsup"}, "plan 0012"},
	}
	for _, c := range cases {
		code, _, stderr := runT(t, clicfg.MapEnv{"SOCIALWEB_HOST_BACKEND": "proc"}, c.goos, c.args...)
		if code != 2 || !strings.Contains(stderr, c.plan) || !strings.Contains(stderr, c.goos) {
			t.Errorf("%s %v: exit %d, %q; want 2 naming %s", c.goos, c.args, code, stderr, c.plan)
		}
	}
	for _, v := range capabilities.Tree() {
		for _, goos := range []string{"windows", "darwin"} {
			if why, missing := capabilities.Missing(v, goos, "arm64"); missing && !strings.Contains(why, "plan 001") {
				t.Errorf("%s on %s: %q names no plan", v.Path, goos, why)
			}
		}
	}
}

func TestServeWithoutAHostBackendExits2NamingTheOption(t *testing.T) {
	code, _, stderr := runT(t, clicfg.MapEnv{}, "linux", "serve", "gateway", "kcp")
	if code != 2 || !strings.Contains(stderr, "host-backend") || !strings.Contains(stderr, "SOCIALWEB_HOST_BACKEND") {
		t.Fatalf("exit %d, %q", code, stderr)
	}
	code, _, stderr = runT(t, clicfg.MapEnv{"SOCIALWEB_HOST_BACKEND": "docker"}, "linux", "serve", "gateway", "kcp")
	if code != 2 || !strings.Contains(stderr, "docker") {
		t.Fatalf("an unknown backend: exit %d, %q", code, stderr)
	}
}

func TestAnUnbuiltSubcommandExits2NamingItsItem(t *testing.T) {
	cases := map[string][]string{
		"M3-SERVE-GATEWAY-KCP":     {"serve", "gateway", "kcp", "--host-backend", "systemd-user"},
		"M1-INSTALL":               {"install", "bidder"},
		"M3-KCP-ROLL":              {"units", "roll", "--host-backend", "systemd-user"},
		"M3-CP-SLOTS":              {"dev", "roll"},
		"plan 0008 M4-RUN-REQUEST": {"run", "request"},
	}
	for item, args := range cases {
		code, _, stderr := runT(t, clicfg.MapEnv{}, "linux", args...)
		if code != 2 || !strings.Contains(stderr, item) {
			t.Errorf("%v: exit %d, %q; want 2 naming %s", args, code, stderr, item)
		}
	}
}

func TestHelpListsTheTreeAndMarksVerbsWithNoBackendOnThisOS(t *testing.T) {
	code, out, _ := runT(t, clicfg.MapEnv{}, "windows", "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, v := range capabilities.Tree() {
		if !strings.Contains(out, v.Path) {
			t.Errorf("help lacks %s", v.Path)
		}
	}
	if !strings.Contains(out, "no backend here") || !strings.Contains(out, "plan 0011") || !strings.Contains(out, "not built: M1-INSTALL") {
		t.Errorf("help does not mark backends and items:\n%s", out)
	}
	if code, _, _ := runT(t, clicfg.MapEnv{}, "linux"); code != 2 {
		t.Errorf("no subcommand: exit %d, want 2", code)
	}
	if code, _, stderr := runT(t, clicfg.MapEnv{}, "linux", "dev"); code != 2 || !strings.Contains(stderr, "dev up") {
		t.Errorf("a bare group: exit %d %q", code, stderr)
	}
}

func TestVersionJSON(t *testing.T) {
	code, out, _ := runT(t, clicfg.MapEnv{}, "linux", "version", "--json")
	var v versionInfo
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v.Name != "socialweb-computer" || v.GOOS != "linux" || v.Go == "" {
		t.Fatalf("exit %d %q", code, out)
	}
}

func TestDevUpNeedsAProfileAndExplicitExecutables(t *testing.T) {
	env := clicfg.MapEnv{"XDG_RUNTIME_DIR": t.TempDir(), "TASK_STATE_DIR": t.TempDir()}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"dev", "up", "--instance", "t1"}, "profile"},
		{[]string{"dev", "up", "--instance", "t1", "--profile", "bed"}, "M2-BOOTSTRAP-LAYER"},
		{[]string{"dev", "up", "--instance", "t1", "--profile", "minimal"}, "kcp-path"},
		{[]string{"dev", "up", "--instance", "t1", "--profile", "minimal", "--kcp-path", "kcp"}, "never looks in PATH"},
		{[]string{"dev", "up", "--host-backend", "systemd-user", "--profile", "minimal"}, "devsup (proc)"},
	}
	for _, c := range cases {
		code, _, stderr := runT(t, env, "linux", c.args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d %q, want 2 naming %q", c.args, code, stderr, c.want)
		}
	}
}

func TestAPhaseSelectionTakesNamesAndOrderedRanges(t *testing.T) {
	ix, err := manifests.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := ix.Set("market-testbed")
	got, err := selectPhases(s, "workspaces..rbac-relay,verifier")
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(phaseNames(manifests.Set{Phases: got}), " "); names != "workspaces rbac-global rbac-relay verifier" {
		t.Fatalf("selected %s", names)
	}
	for _, bad := range []string{"nope", "relay..workspaces", "workspaces..nope"} {
		if _, err := selectPhases(s, bad); err == nil {
			t.Errorf("%q selected phases", bad)
		}
	}
}
