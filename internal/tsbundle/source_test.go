package tsbundle

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
)

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func checkoutFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range checkoutMarkers {
		p := filepath.Join(root, filepath.FromSlash(m))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestTheCheckoutIsFoundByWalkingUp(t *testing.T) {
	root := checkoutFixture(t)
	deep := filepath.Join(root, "ts", "a", "b", "c")
	mkdirs(t, deep)
	got, err := FindCheckout(deep)
	if err != nil || got != root {
		t.Fatalf("from %s found %q (%v), want %s", deep, got, err, root)
	}
	got, err = FindCheckout(t.TempDir(), deep)
	if err != nil || got != root {
		t.Fatalf("the executable's directory was not walked after the working directory: %q (%v)", got, err)
	}
	half := t.TempDir()
	if err := os.WriteFile(filepath.Join(half, WorkspaceFile), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckCheckout(half); err == nil {
		t.Fatalf("a directory with %s and no %s was accepted", WorkspaceFile, EntriesFile)
	}
	if _, err := FindCheckout(t.TempDir()); err == nil {
		t.Fatal("a tree with no checkout above it found one")
	}
	if wd, err := os.Getwd(); err == nil {
		found, err := FindCheckout(wd)
		if err != nil {
			t.Fatalf("this package sits in no checkout: %v", err)
		}
		rel, _ := filepath.Rel(found, wd)
		if filepath.ToSlash(rel) != "internal/tsbundle" {
			t.Fatalf("from this package (%s) the walk found %s", wd, found)
		}
	}
}

func TestADevBuildTakesTheSourceRootOptionOverTheWalk(t *testing.T) {
	if clicfg.ReleaseBuild() {
		t.Skip("a release build has no source-root option")
	}
	spec, err := clicfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	o, ok := spec.Options[SourceRootOption]
	if !ok || o.Env != SourceRootEnv {
		t.Fatalf("options.json %s = %+v (present %v), want env %s", SourceRootOption, o, ok, SourceRootEnv)
	}
	root := checkoutFixture(t)
	vals, _, err := spec.Resolve("t", []string{SourceRootOption}, nil, clicfg.MapEnv{SourceRootEnv: root}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	src, err := Select(vals, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind() != KindCheckout || src.Production() {
		t.Fatalf("dev source = %s (production %v)", src.Kind(), src.Production())
	}
	p, err := src.Prepare(t.TempDir())
	if err != nil || p.Root != root {
		t.Fatalf("prepared %+v (%v), want root %s", p, err, root)
	}
	other := checkoutFixture(t)
	p2, _ := Select(clicfg.Values{}, filepath.Join(other, "ts"))
	pp2, err := p2.Prepare(t.TempDir())
	if err != nil || pp2.Root != other || pp2.Key == p.Key {
		t.Fatalf("two checkouts must key two DENO_DIRs: %+v %+v (%v)", p, pp2, err)
	}
}

func TestAProductionBuildHasNoSourceRootOption(t *testing.T) {
	if !clicfg.ReleaseBuild() {
		if testing.Short() {
			t.Skip("builds this package with -tags release")
		}
		cmd := exec.Command("go", "test", "-tags", "release", "-count=1", "-v", "-run", "^TestAProductionBuildHasNoSourceRootOption$", ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestAProductionBuildHasNoSourceRootOption") {
			t.Fatalf("the release build of this test failed: %v\n%s", err, out)
		}
		return
	}
	spec, err := clicfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Options[SourceRootOption]; ok {
		t.Fatalf("a release build defines option %s", SourceRootOption)
	}
	if n, ok := spec.EnvNames()[SourceRootEnv]; ok {
		t.Fatalf("a release build reads %s (option %s)", SourceRootEnv, n)
	}
	if _, _, err := spec.Resolve("t", []string{SourceRootOption}, nil, clicfg.MapEnv{}, nil, io.Discard); err == nil {
		t.Fatalf("a release verb could name option %s", SourceRootOption)
	}
	root := checkoutFixture(t)
	t.Setenv(SourceRootEnv, root)
	src, err := Select(clicfg.Values{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind() != KindEmbedded || !src.Production() {
		t.Fatalf("a release build selected %s", src.Kind())
	}
}

func TestStageUnitIsTheOriginDirectory(t *testing.T) {
	for in, want := range map[string]string{
		"ts/atproto-market/hono-bidder/mod.ts":                      "ts/atproto-market",
		"socialweb-computer-k8s/hono-socialweb-computer-k8s/mod.ts": "socialweb-computer-k8s",
		"deno.json": "deno.json",
		"ts/x.ts":   "ts/x.ts",
	} {
		if got := StageUnit(in); got != want {
			t.Errorf("StageUnit(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "/abs/x.ts", "../escape.ts", "a/../b.ts", "."} {
		if CleanPath(bad) == nil {
			t.Errorf("CleanPath(%q) accepted", bad)
		}
	}
}
