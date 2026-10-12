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

func orgRootFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mkdirs(t, root, SiblingRepos...)
	return root
}

func TestTheOrgRootIsFoundByWalkingUpAndASymlinkedSiblingIsRefused(t *testing.T) {
	root := orgRootFixture(t)
	deep := filepath.Join(root, "socialweb-computer-kcp", "a", "b", "c")
	mkdirs(t, deep)
	got, err := FindOrgRoot(deep)
	if err != nil || got != root {
		t.Fatalf("from %s found %q (%v), want %s", deep, got, err, root)
	}
	got, err = FindOrgRoot(t.TempDir(), deep)
	if err != nil || got != root {
		t.Fatalf("the executable's directory was not walked after the working directory: %q (%v)", got, err)
	}
	task := filepath.Join(root, ".claude", "worktrees", "t1")
	mkdirs(t, task, SiblingRepos...)
	for _, s := range SiblingRepos {
		if err := os.WriteFile(filepath.Join(task, s, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inTask := filepath.Join(task, "socialweb-computer-kcp", "x", "y", "z")
	mkdirs(t, inTask)
	if got, err := FindOrgRoot(inTask); err != nil || got != task {
		t.Fatalf("a task tree is an org root: found %q (%v), want %s", got, err, task)
	}
	bad := filepath.Join(root, "linked")
	mkdirs(t, bad, "typescript-helpers", "socialweb-computer-kcp/x")
	if err := os.Symlink(filepath.Join(root, "atproto-market"), filepath.Join(bad, "atproto-market")); err != nil {
		t.Fatal(err)
	}
	_, err = FindOrgRoot(filepath.Join(bad, "socialweb-computer-kcp", "x"))
	if err == nil || !strings.Contains(err.Error(), "atproto-market") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlinked sibling was not refused by name: %v", err)
	}
	if _, err := FindOrgRoot(t.TempDir()); err == nil {
		t.Fatal("a tree with no org root above it found one")
	}
	if _, err := CheckOrgRoot(bad); err == nil {
		t.Fatal("an explicit org root with a symlinked sibling was accepted")
	}
	if wd, err := os.Getwd(); err == nil {
		if found, err := FindOrgRoot(wd); err == nil {
			rel, _ := filepath.Rel(found, wd)
			if filepath.ToSlash(rel) != "socialweb-computer-kcp/internal/tsbundle" {
				t.Fatalf("from this package (%s) the walk found %s", wd, found)
			}
		}
	}
}

func TestADevBuildTakesTheOrgRootOptionOverTheWalk(t *testing.T) {
	if clicfg.ReleaseBuild() {
		t.Skip("a release build has no org-root option")
	}
	spec, err := clicfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	o, ok := spec.Options[OrgRootOption]
	if !ok || o.Env != OrgRootEnv {
		t.Fatalf("options.json %s = %+v (present %v), want env %s", OrgRootOption, o, ok, OrgRootEnv)
	}
	root := orgRootFixture(t)
	vals, _, err := spec.Resolve("t", []string{OrgRootOption}, nil, clicfg.MapEnv{OrgRootEnv: root}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	src, err := Select(vals, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind() != KindOrgRoot || src.Production() {
		t.Fatalf("dev source = %s (production %v)", src.Kind(), src.Production())
	}
	p, err := src.Prepare(t.TempDir())
	if err != nil || p.Root != root {
		t.Fatalf("prepared %+v (%v), want root %s", p, err, root)
	}
	other := orgRootFixture(t)
	p2, _ := Select(clicfg.Values{}, filepath.Join(other, "atproto-market"))
	pp2, err := p2.Prepare(t.TempDir())
	if err != nil || pp2.Root != other || pp2.Key == p.Key {
		t.Fatalf("two org roots must key two DENO_DIRs: %+v %+v (%v)", p, pp2, err)
	}
}

func TestAProductionBuildHasNoOrgRootOption(t *testing.T) {
	if !clicfg.ReleaseBuild() {
		if testing.Short() {
			t.Skip("builds this package with -tags release")
		}
		cmd := exec.Command("go", "test", "-tags", "release", "-count=1", "-v", "-run", "^TestAProductionBuildHasNoOrgRootOption$", ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestAProductionBuildHasNoOrgRootOption") {
			t.Fatalf("the release build of this test failed: %v\n%s", err, out)
		}
		return
	}
	spec, err := clicfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spec.Options[OrgRootOption]; ok {
		t.Fatalf("a release build defines option %s", OrgRootOption)
	}
	if n, ok := spec.EnvNames()[OrgRootEnv]; ok {
		t.Fatalf("a release build reads %s (option %s)", OrgRootEnv, n)
	}
	if _, _, err := spec.Resolve("t", []string{OrgRootOption}, nil, clicfg.MapEnv{}, nil, io.Discard); err == nil {
		t.Fatalf("a release verb could name option %s", OrgRootOption)
	}
	root := orgRootFixture(t)
	t.Setenv(OrgRootEnv, root)
	src, err := Select(clicfg.Values{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind() != KindEmbedded || !src.Production() {
		t.Fatalf("a release build selected %s", src.Kind())
	}
}
