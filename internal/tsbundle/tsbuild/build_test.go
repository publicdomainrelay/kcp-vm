package tsbuild

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

func denoBinary(t *testing.T) string {
	t.Helper()
	p := os.Getenv("SOCIALWEB_DENO_PATH")
	if p == "" {
		found, err := exec.LookPath("deno")
		if err != nil {
			livegate.RequireLocal(t, "deno %s is not on this host (set SOCIALWEB_DENO_PATH)", denorun.PinnedVersion())
			return ""
		}
		p = found
	}
	if err := denorun.CheckVersion(p); err != nil {
		livegate.RequireLocal(t, "%v", err)
	}
	return p
}

func put(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_AUTHOR_DATE=2026-10-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-10-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

var fixtureEntries = []tsbundle.EntrySpec{
	{Name: "web", Entry: denorun.Entry{Path: "svc/web/mod.ts", Preload: []string{"svc/web/preload.ts"}}},
	{Name: "market", Entry: denorun.Entry{Path: "ts/atproto-market/hono-thing/mod.ts", Unstable: []string{"kv"}}},
}

func closureFixture(t *testing.T, deno string) (string, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/typescript")
		w.Header().Set("X-Request-Time", time.Now().Format(time.RFC3339Nano))
		fmt.Fprintf(w, "export const remote: string = %q;\n", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	root := t.TempDir()
	put(t, filepath.Join(root, "deno.json"), "{\n  \"workspace\": [\"./ts/typescript-helpers/lib/x\", \"./ts/other/lib/y\"]\n}\n")
	put(t, filepath.Join(root, "deploy", "ts-entries.yaml"), "entries: []\n")
	put(t, filepath.Join(root, "ts", "typescript-helpers", "lib", "x", "deno.json"), "{\"name\": \"@fixture/x\", \"version\": \"0.0.0\", \"exports\": \"./mod.ts\"}\n")
	put(t, filepath.Join(root, "ts", "typescript-helpers", "lib", "x", "mod.ts"), "export function hello(): string {\n  return \"hello-from-helpers\";\n}\n")
	put(t, filepath.Join(root, "ts", "other", "lib", "y", "deno.json"), "{\"name\": \"@fixture/y\", \"version\": \"0.0.0\", \"exports\": \"./mod.ts\"}\n")
	put(t, filepath.Join(root, "ts", "other", "lib", "y", "mod.ts"), "export const y = 1;\n")
	put(t, filepath.Join(root, "ts", "atproto-market", "hono-thing", "mod.ts"),
		fmt.Sprintf("import { hello } from \"../../typescript-helpers/lib/x/mod.ts\";\nimport { remote } from %q;\nif (Deno.args.includes(\"--help\")) console.log(\"market\", hello(), remote);\n", srv.URL+"/remote.ts"))
	put(t, filepath.Join(root, "main.go"), "package main\n")
	put(t, filepath.Join(root, "svc", "web", "preload.ts"), "globalThis.preloaded = true;\n")
	put(t, filepath.Join(root, "svc", "web", "mod.ts"),
		"import { hello } from \"../../ts/atproto-market/../typescript-helpers/lib/x/mod.ts\";\nif (Deno.args.includes(\"--help\")) console.log(\"web\", hello());\n")
	dd := t.TempDir()
	for _, e := range fixtureEntries {
		s := denorun.Spec{Deno: deno, Mode: denorun.Dev, DenoDirs: dd, Key: strings.Repeat("0", 64), Dir: root, Entry: e.Entry}
		if out, err := s.Install(context.Background(), []string{"HOME=" + os.Getenv("HOME")}, denorun.InstallOptions{AllowImport: []string{host}}); err != nil {
			t.Fatalf("fixture lockfile: %v\n%s", err, out)
		}
	}
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "fixture")
	return root, host
}

func buildFixture(t *testing.T, deno, root, host string, release bool) (string, tsbundle.Manifest, error) {
	t.Helper()
	out := t.TempDir()
	m, err := Build(context.Background(), BuildOptions{Root: root, Entries: fixtureEntries, Deno: deno, Release: release,
		Work: writableTempDir(t), Out: out, Offline: unshareWorks(), AllowImport: []string{host}})
	return out, m, err
}

func writableTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	return dir
}

func unshareWorks() bool {
	return exec.Command("unshare", "-rn", "true").Run() == nil
}

func TestTwoBundlesOfTheSameCommitsAreByteIdentical(t *testing.T) {
	deno := denoBinary(t)
	root, host := closureFixture(t, deno)
	out1, m, err := buildFixture(t, deno, root, host, true)
	if err != nil {
		t.Fatal(err)
	}
	out2, _, err := buildFixture(t, deno, root, host, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tsbundle.ArchiveName, tsbundle.ManifestName} {
		a, err1 := os.ReadFile(filepath.Join(out1, name))
		b, err2 := os.ReadFile(filepath.Join(out2, name))
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: %v %v", name, err1, err2)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("two bundles of the same commits differ in %s", name)
		}
	}
	var paths []string
	for _, f := range m.Files {
		paths = append(paths, f.Path)
	}
	for _, want := range []string{"src/ts/typescript-helpers/lib/x/mod.ts", "src/svc/web/preload.ts", "src/deno.lock", "src/deno.json", "src/ts/other/lib/y/mod.ts"} {
		if !slices.Contains(paths, want) {
			t.Errorf("the closure lacks %s", want)
		}
	}
	if slices.Contains(paths, "src/main.go") {
		t.Error("the closure carries main.go, which no entry reaches")
	}
	if len(m.Repos) != 1 || m.Repos[0].Name != tsbundle.RepoName {
		t.Errorf("manifest repos = %+v, want the one checkout", m.Repos)
	}
	for _, r := range m.Repos {
		if len(r.Commit) != 40 || r.Dirty {
			t.Errorf("repo %+v: want its 40-hex commit and clean", r)
		}
	}
	for _, e := range m.Entries {
		if !e.OK {
			t.Errorf("entry %s did not start from the extracted tree: %s", e.Name, e.Reason)
		}
	}
	if !m.Release || m.Platform == "" || m.Deno == "" || m.TreeSHA256 == "" {
		t.Errorf("manifest header %+v", m)
	}
}

func TestADirtyCheckoutIsRefusedForARelease(t *testing.T) {
	deno := denoBinary(t)
	root, host := closureFixture(t, deno)
	put(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")
	if _, _, err := buildFixture(t, deno, root, host, true); err != nil {
		t.Fatalf("a change outside every staged prefix refused the release: %v", err)
	}
	put(t, filepath.Join(root, "ts", "typescript-helpers", "lib", "x", "mod.ts"), "export function hello(): string {\n  return \"dirty\";\n}\n")
	_, _, err := buildFixture(t, deno, root, host, true)
	if err == nil || !strings.Contains(err.Error(), "ts/typescript-helpers/lib/x/mod.ts") || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("a dirty staged path was not refused by name for a release: %v", err)
	}
	_, m, err := buildFixture(t, deno, root, host, false)
	if err != nil {
		t.Fatalf("a dev bundle of a dirty tree: %v", err)
	}
	for _, r := range m.Repos {
		if !r.Dirty {
			t.Fatalf("the dev manifest does not record the checkout as dirty: %+v", r)
		}
	}
}

func TestLoadEntriesRefusesAnEscapeAndAnUnknownField(t *testing.T) {
	if _, err := LoadEntries(writeEntries(t, "entries:\n  - name: x\n    path: ../escape.ts\n")); err == nil {
		t.Error("an entry outside the checkout was accepted")
	}
	if _, err := LoadEntries(writeEntries(t, "entries:\n  - name: x\n    path: a/m.ts\n    bogus: 1\n")); err == nil {
		t.Error("an unknown entry field was accepted")
	}
}

func writeEntries(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "entries.yaml")
	put(t, p, body)
	return p
}
