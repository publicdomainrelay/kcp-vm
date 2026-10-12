package denorun

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func denoBinary(t *testing.T) string {
	t.Helper()
	p := os.Getenv("SOCIALWEB_DENO_PATH")
	if p == "" {
		found, err := exec.LookPath("deno")
		if err != nil {
			livegate.RequireLocal(t, "deno %s is not on this host (set SOCIALWEB_DENO_PATH)", PinnedVersion())
			return ""
		}
		p = found
	}
	if err := CheckVersion(p); err != nil {
		livegate.RequireLocal(t, "%v", err)
	}
	return p
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func treeKey(t *testing.T, dir string) string {
	t.Helper()
	k, err := assets.TreeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func runSpec(t *testing.T, s Spec, wrap ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd, err := s.Command(ctx, []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	if len(wrap) > 0 {
		args := append(append([]string{}, wrap[1:]...), cmd.Args...)
		inner := exec.CommandContext(ctx, wrap[0], args...)
		inner.Dir, inner.Env = cmd.Dir, cmd.Env
		cmd = inner
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	if err != nil {
		return out.String() + errb.String(), fmt.Errorf("%v: %s", err, errb.String())
	}
	return out.String(), nil
}

func TestEveryArgvCarriesNoCodeCacheAndNoNodeModules(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, mode := range []Mode{Dev, Production} {
		s := Spec{Deno: "/opt/deno", Mode: mode, DenoDirs: "/state/deno", Key: key, Dir: "/src",
			Entry: Entry{Path: "a/mod.ts", Preload: []string{"a/pre.ts"}, Unstable: []string{"kv", "worker-options"}}, Args: []string{"--help"}}
		argv, err := s.Argv()
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"/opt/deno", "run", "--no-code-cache", "--node-modules-dir=none"}
		if mode == Production {
			want = append(want, "--cached-only", "--frozen")
		}
		want = append(want, "-A", "--unstable-kv", "--unstable-worker-options", "--preload", "a/pre.ts", "a/mod.ts", "--help")
		if !slices.Equal(argv, want) {
			t.Errorf("%s argv = %q, want %q", mode, argv, want)
		}
		env, err := s.Env([]string{"DENO_DIR=/elsewhere", "DENO_NO_UPDATE_CHECK=0", "HOME=/h"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(env, "DENO_DIR=/state/deno/"+key) || slices.Contains(env, "DENO_DIR=/elsewhere") {
			t.Errorf("%s env = %q: DENO_DIR must be keyed by the closure", mode, env)
		}
		if got := slices.Contains(env, "DENO_NO_UPDATE_CHECK=1"); got != (mode == Production) || slices.Contains(env, "DENO_NO_UPDATE_CHECK=0") {
			t.Errorf("%s env = %q: DENO_NO_UPDATE_CHECK=1 is production's", mode, env)
		}
	}
	if _, err := (Spec{Deno: "/opt/deno", Mode: Production, DenoDirs: "/state/deno", Key: "../x", Entry: Entry{Path: "m.ts"}}).Argv(); err == nil {
		t.Fatal("a key that is not a sha256 named a DENO_DIR")
	}
	if _, err := (Spec{Deno: "deno", Mode: Production, DenoDirs: "/state/deno", Key: key, Entry: Entry{Path: "m.ts"}}).Argv(); err == nil {
		t.Fatal("a relative deno path was accepted; deno is never found on PATH")
	}
}

func TestAnUpgradedClosureNeverRunsTheOldCodeCache(t *testing.T) {
	deno := denoBinary(t)
	state := filepath.Join(t.TempDir(), "deno")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "deno.json"), "{}\n")
	writeFile(t, filepath.Join(src, "main.ts"), "export const v: string = \"closure-old\";\nconsole.log(v);\n")
	oldKey := treeKey(t, src)
	spec := Spec{Deno: deno, Mode: Production, DenoDirs: state, Key: oldKey, Dir: src, Entry: Entry{Path: "main.ts"}}
	for range 2 {
		out, err := runSpec(t, spec)
		if err != nil || strings.TrimSpace(out) != "closure-old" {
			t.Fatalf("old closure printed %q (%v)", out, err)
		}
	}
	oldDir, _ := spec.DenoDir()
	writeFile(t, filepath.Join(src, "main.ts"), "export const v: string = \"closure-new\";\nconsole.log(v);\n")
	newKey := treeKey(t, src)
	if newKey == oldKey {
		t.Fatal("a changed source kept its closure sha256")
	}
	upgraded := spec
	upgraded.Key = newKey
	out, err := runSpec(t, upgraded)
	if err != nil || strings.TrimSpace(out) != "closure-new" {
		t.Fatalf("upgraded closure printed %q (%v); it ran old code", out, err)
	}
	newDir, _ := upgraded.DenoDir()
	if newDir == oldDir {
		t.Fatalf("both closures share DENO_DIR %s", newDir)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("the old DENO_DIR must still be on disk for this proof: %v", err)
	}
	out, err = runSpec(t, spec)
	if err != nil || strings.TrimSpace(out) != "closure-new" {
		t.Fatalf("the new source run against the old DENO_DIR printed %q (%v)", out, err)
	}
	for _, d := range []string{oldDir, newDir} {
		matches, _ := filepath.Glob(filepath.Join(d, "v8_code_cache*"))
		if len(matches) > 0 {
			t.Errorf("DENO_DIR %s holds a v8 code cache %v; --no-code-cache must keep it empty", d, matches)
		}
	}
}

func TestAProductionRunNeverReachesTheNetworkForItsModules(t *testing.T) {
	deno := denoBinary(t)
	if err := exec.Command("unshare", "-rn", "true").Run(); err != nil {
		livegate.RequireLocal(t, "unshare -rn is not available on this host: %v", err)
	}
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/typescript")
		fmt.Fprintf(w, "export const v: string = %q;\n", "remote"+r.URL.Path)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	state := filepath.Join(t.TempDir(), "deno")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "deno.json"), "{}\n")
	writeFile(t, filepath.Join(src, "main.ts"), fmt.Sprintf("import { v } from %q;\nconsole.log(v);\n", srv.URL+"/m.ts"))
	key := treeKey(t, src)
	fill := Spec{Deno: deno, Mode: Production, DenoDirs: state, Key: key, Dir: src, Entry: Entry{Path: "main.ts"}}
	if out, err := fill.Install(context.Background(), []string{"HOME=" + os.Getenv("HOME")}, InstallOptions{AllowImport: []string{host}}); err != nil {
		t.Fatalf("filling the DENO_DIR: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(src, "deno.lock")); err != nil {
		t.Fatalf("the fill wrote no lockfile for --frozen to hold: %v", err)
	}
	filled := hits.Load()
	if filled == 0 {
		t.Fatal("the fill never fetched the remote module")
	}
	out, err := runSpec(t, fill, "unshare", "-rn")
	if err != nil || strings.TrimSpace(out) != "remote/m.ts" {
		t.Fatalf("an offline production run printed %q (%v)", out, err)
	}
	writeFile(t, filepath.Join(src, "uncached.ts"), fmt.Sprintf("import { v } from %q;\nconsole.log(v);\n", srv.URL+"/n.ts"))
	uncached := fill
	uncached.Entry = Entry{Path: "uncached.ts"}
	if out, err := runSpec(t, uncached); err == nil {
		t.Fatalf("a production run of an uncached module succeeded: %q", out)
	}
	if got := hits.Load(); got != filled {
		t.Fatalf("production runs reached the module server %d times; want none", got-filled)
	}
	dev := uncached
	dev.Mode = Dev
	dev.Entry.Path = "main.ts"
	if out, err := runSpec(t, dev); err != nil || strings.TrimSpace(out) != "remote/m.ts" {
		t.Fatalf("a dev run printed %q (%v)", out, err)
	}
}

func TestCollectKeepsTheNamedClosuresAndRemovesTheRest(t *testing.T) {
	root := t.TempDir()
	keep := strings.Repeat("1", 64)
	gone := strings.Repeat("2", 64)
	for _, k := range []string{keep, gone} {
		writeFile(t, filepath.Join(root, k, "remote", "x"), "x")
	}
	writeFile(t, filepath.Join(root, "notes"), "n")
	removed, err := Collect(root, []string{keep})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{gone}) {
		t.Fatalf("removed %v, want [%s]", removed, gone)
	}
	if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
		t.Fatal("a kept closure was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "notes")); err != nil {
		t.Fatal("a non-closure entry was removed")
	}
}
