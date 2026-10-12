package denokcp

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

var checkoutEnv = map[string]string{"deno-kcp": "DENO_KCP_DIR", "kcp-libs": "KCP_LIBS_DIR"}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func findCheckout(repoRoot, name string) (string, bool) {
	if dir := os.Getenv(checkoutEnv[name]); dir != "" {
		return dir, true
	}
	dir := repoRoot
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(candidate, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(candidate, ".git")); err == nil {
				return candidate, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func upstreamGoFiles(checkout, commit, dir string) ([]string, error) {
	out, err := exec.Command("git", "-C", checkout, "ls-tree", "--name-only", commit+":"+dir).Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Fields(string(out)) {
		if strings.HasSuffix(name, ".go") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func TestTheForkRecordsTheUpstreamCommitsItStartedFrom(t *testing.T) {
	root := repoRoot(t)
	fork, err := LoadFork(root)
	if err != nil {
		t.Fatalf("the fork of deno-kcp and kcp-libs has no readable record of where it came from: %v", err)
	}

	for _, name := range []string{"deno-kcp", "kcp-libs"} {
		u, ok := fork.Upstreams[name]
		if !ok {
			t.Fatalf("%s records no %s upstream; a later upstream change is pulled by cherry-pick from the recorded commit", RecordPath, name)
		}
		if !fullCommit.MatchString(u.Commit) {
			t.Errorf("%s upstream commit %q is not a full 40-hex commit", name, u.Commit)
		}
		if u.Repository == "" || u.Module == "" {
			t.Errorf("%s upstream names no repository or module", name)
		}
	}
	if fork.Adapted == "" {
		t.Error("the fork record does not say how the copy differs from its upstream")
	}

	recorded := map[string]Package{}
	for _, p := range fork.Packages {
		if _, dup := recorded[p.Path]; dup {
			t.Errorf("%s is recorded twice", p.Path)
		}
		recorded[p.Path] = p
		if _, ok := fork.Upstreams[p.From]; !ok {
			t.Errorf("%s comes from %q, which is not a recorded upstream", p.Path, p.From)
		}
		if len(goFiles(t, filepath.Join(root, p.Path))) == 0 {
			t.Errorf("%s is recorded and holds no Go file", p.Path)
		}
	}
	local := map[string]bool{}
	for _, l := range fork.Local {
		local[l] = true
		if _, err := os.Stat(filepath.Join(root, l)); err != nil {
			t.Errorf("%s is recorded as this repo's own and is missing: %v", l, err)
		}
	}
	for _, p := range fork.Patches {
		if _, err := os.Stat(filepath.Join(root, p.Patch)); err != nil {
			t.Errorf("patch %s is recorded and missing: %v", p.Patch, err)
		}
		if _, ok := fork.Upstreams[p.From]; !ok {
			t.Errorf("patch %s applies to %q, which is not a recorded upstream", p.Patch, p.From)
		}
	}

	upstreamModules := map[string]bool{}
	for _, u := range fork.Upstreams {
		upstreamModules[u.Module] = true
	}
	for _, base := range ForkRoots {
		err := filepath.WalkDir(filepath.Join(root, base), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, ok := recorded[filepath.ToSlash(filepath.Dir(rel))]; !ok && !local[rel] {
				t.Errorf("%s is in the fork and in no recorded package, so nothing says where it came from", rel)
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				return nil
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				for m := range upstreamModules {
					if p == m || strings.HasPrefix(p, m+"/") {
						t.Errorf("%s still imports %s from upstream; the fork owns its copy", rel, p)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"deno-kcp", "kcp-libs"} {
		t.Run(name, func(t *testing.T) {
			u := fork.Upstreams[name]
			checkout, ok := findCheckout(root, name)
			if !ok {
				t.Skipf("no %s checkout beside this repository (set %s); the record cannot be compared with %s", name, checkoutEnv[name], u.Commit)
			}
			if err := exec.Command("git", "-C", checkout, "cat-file", "-e", u.Commit+"^{commit}").Run(); err != nil {
				t.Skipf("%s commit %s is not in %s; fetch %s to compare", name, u.Commit, checkout, u.Repository)
			}
			for _, p := range fork.Packages {
				if p.From != name {
					continue
				}
				want, err := upstreamGoFiles(checkout, u.Commit, p.Upstream)
				if err != nil {
					t.Errorf("%s: %s has no %s at %s: %v", p.Path, name, p.Upstream, u.Commit, err)
					continue
				}
				want = append(want, p.Added...)
				sort.Strings(want)
				if have := goFiles(t, filepath.Join(root, p.Path)); strings.Join(have, " ") != strings.Join(want, " ") {
					t.Errorf("%s holds %v and %s %s:%s plus the recorded additions is %v", p.Path, have, name, u.Commit, p.Upstream, want)
				}
			}
		})
	}
}
