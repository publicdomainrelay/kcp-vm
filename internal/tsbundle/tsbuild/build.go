package tsbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

const (
	LockFrozen    = "frozen"
	LockWorkspace = "workspace-refreshed"
	LockDrifted   = "drifted"
	LockCreated   = "created"
	fillKey       = "0000000000000000000000000000000000000000000000000000000000000000"
)

func configRoot(root, p string) string {
	repo, rest, _ := tsbundle.SplitRepo(p)
	dir := path.Dir(rest)
	found := dir
	for d := dir; ; d = path.Dir(d) {
		if hasConfig(filepath.Join(root, repo, filepath.FromSlash(d))) {
			found = d
		}
		if d == "." {
			break
		}
	}
	return path.Join(repo, found)
}

func resolveDirs(root string, entries []tsbundle.EntrySpec) ([]tsbundle.EntrySpec, error) {
	out := make([]tsbundle.EntrySpec, len(entries))
	for i, e := range entries {
		if e.Dir == "" {
			e.Dir = configRoot(root, e.Path)
		}
		if len(e.Probe) == 0 {
			e.Probe = slices.Clone(tsbundle.DefaultProbe)
		}
		if _, _, err := tsbundle.SplitRepo(e.Dir + "/x"); err != nil {
			return nil, fmt.Errorf("tsbundle: entry %s dir: %w", e.Name, err)
		}
		out[i] = e
	}
	return out, nil
}

type entriesFile struct {
	Entries []tsbundle.EntrySpec `yaml:"entries"`
}

func LoadEntries(p string) ([]tsbundle.EntrySpec, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var f entriesFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("tsbundle: %s: %w", p, err)
	}
	if err := tsbundle.CheckEntries(f.Entries); err != nil {
		return nil, fmt.Errorf("tsbundle: %s: %w", p, err)
	}
	return f.Entries, nil
}

type BuildOptions struct {
	OrgRoot      string
	Entries      []tsbundle.EntrySpec
	Deno         string
	Release      bool
	Offline      bool
	Epoch        int64
	Work         string
	Out          string
	AllowImport  []string
	ProbeTimeout time.Duration
	Log          io.Writer
}

func (o BuildOptions) logf(format string, args ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, "socialweb-tsbundle: "+format+"\n", args...)
	}
}

func Build(ctx context.Context, o BuildOptions) (tsbundle.Manifest, error) {
	var m tsbundle.Manifest
	if err := tsbundle.CheckEntries(o.Entries); err != nil {
		return m, fmt.Errorf("tsbundle: %w", err)
	}
	root, err := tsbundle.CheckOrgRoot(o.OrgRoot)
	if err != nil {
		return m, err
	}
	if !filepath.IsAbs(o.Deno) {
		return m, fmt.Errorf("tsbundle: deno path %q is not absolute; deno is never found on PATH", o.Deno)
	}
	if err := denorun.CheckVersion(o.Deno); err != nil {
		return m, err
	}
	if o.Work == "" || o.Out == "" {
		return m, errors.New("tsbundle: a work and an out directory are required")
	}
	if o.ProbeTimeout == 0 {
		o.ProbeTimeout = time.Minute
	}
	work, err := filepath.Abs(o.Work)
	if err != nil {
		return m, err
	}
	if o.Entries, err = resolveDirs(root, o.Entries); err != nil {
		return m, err
	}
	o.logf("graph of %d entries under %s", len(o.Entries), root)
	files, err := graphFiles(ctx, o, root, filepath.Join(work, "graph"))
	if err != nil {
		return m, err
	}
	prefixes, err := stagePrefixes(root, files)
	if err != nil {
		return m, err
	}
	tree := filepath.Join(work, "tree")
	src := filepath.Join(tree, tsbundle.SourceDir)
	repos, epoch, err := stageRepos(ctx, o, root, src, prefixes)
	if err != nil {
		return m, err
	}
	if o.Epoch != 0 {
		epoch = o.Epoch
	}
	o.logf("staged %d repos; filling one DENO_DIR", len(repos))
	locks, before, err := snapshotLocks(src, prefixes)
	if err != nil {
		return m, err
	}
	fill := denorun.Spec{Deno: o.Deno, Mode: denorun.Dev, DenoDirs: work, Key: fillKey}
	if err := fillCache(ctx, o, src, fill); err != nil {
		return m, err
	}
	lockRecs, pins, err := classifyLocks(src, locks, before)
	if err != nil {
		return m, err
	}
	if o.Release {
		var bad []string
		for _, l := range lockRecs {
			if l.State == LockCreated || l.State == LockDrifted {
				bad = append(bad, l.Path+" ("+l.State+")")
			}
		}
		if len(bad) > 0 {
			return m, fmt.Errorf("tsbundle: a release needs every lockfile committed and current; %s (commit the refreshed lockfile in its repo)", strings.Join(bad, ", "))
		}
	}
	if err := seedFrom(filepath.Join(work, fillKey), filepath.Join(tree, tsbundle.SeedDir), pins, epoch); err != nil {
		return m, err
	}
	archive, pm, err := Pack(tree, epoch)
	if err != nil {
		return m, err
	}
	m = pm
	m.Platform = runtime.GOOS + "/" + runtime.GOARCH
	m.Deno = denorun.PinnedVersion()
	m.Release, m.Offline = o.Release, o.Offline
	m.Repos, m.Lockfiles = repos, lockRecs
	o.logf("archive %d bytes, tree %s; starting every entry from the extracted tree", m.ArchiveBytes, m.TreeSHA256)
	m.Entries, err = probe(ctx, o, work, archive, m)
	if err != nil {
		return m, err
	}
	if o.Release {
		var failed []string
		for _, e := range m.Entries {
			if !e.OK {
				failed = append(failed, e.Name+": "+e.Reason)
			}
		}
		if len(failed) > 0 {
			return m, fmt.Errorf("tsbundle: entries do not start offline from the extracted tree: %s", strings.Join(failed, "; "))
		}
	}
	return m, WriteBundle(o.Out, archive, m)
}

type denoInfo struct {
	Modules []struct {
		Specifier string `json:"specifier"`
		Error     string `json:"error"`
	} `json:"modules"`
}

func graphFiles(ctx context.Context, o BuildOptions, root, denoDir string) ([]string, error) {
	set := map[string]bool{}
	for _, e := range o.Entries {
		cwd, en := e.At(root)
		for i, p := range append([]string{en.Path}, en.Preload...) {
			args := []string{"info", "--json", "--no-lock", "--node-modules-dir=none"}
			if len(o.AllowImport) > 0 {
				args = append(args, "--allow-import="+strings.Join(o.AllowImport, ","))
			}
			if en.Config != "" {
				args = append(args, "--config", en.Config)
			}
			cmd := exec.CommandContext(ctx, o.Deno, append(args, p)...)
			cmd.Dir = cwd
			cmd.Env = []string{"DENO_DIR=" + denoDir, "DENO_NO_UPDATE_CHECK=1", "NO_COLOR=1", "HOME=" + os.Getenv("HOME")}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("tsbundle: deno info %s: %w: %s", p, err, strings.TrimSpace(stderr.String()))
			}
			var info denoInfo
			if err := json.Unmarshal(out, &info); err != nil {
				return nil, fmt.Errorf("tsbundle: deno info %s: %w", p, err)
			}
			for _, mod := range info.Modules {
				if mod.Error != "" {
					o.logf("entry %s: %s is not in the graph (%s); the fill and the offline start decide whether it matters", e.Name, mod.Specifier, mod.Error)
					continue
				}
				if !strings.HasPrefix(mod.Specifier, "file://") {
					continue
				}
				u, err := url.Parse(mod.Specifier)
				if err != nil {
					return nil, err
				}
				rel, err := filepath.Rel(root, filepath.FromSlash(u.Path))
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return nil, fmt.Errorf("tsbundle: entry %s reaches %s outside the org root %s", e.Name, u.Path, root)
				}
				set[filepath.ToSlash(rel)] = true
			}
			set[append([]string{e.Path}, e.Preload...)[i]] = true
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

func stagePrefixes(root string, files []string) (map[string][]string, error) {
	byRepo := map[string][]string{}
	for _, f := range files {
		repo, rest, err := tsbundle.SplitRepo(f)
		if err != nil {
			return nil, err
		}
		st, err := os.Lstat(filepath.Join(root, repo))
		if err != nil {
			return nil, err
		}
		if st.Mode()&fs.ModeSymlink != 0 || !st.IsDir() {
			return nil, fmt.Errorf("tsbundle: repo %s is a symlink or not a directory; refused", repo)
		}
		prefix := path.Dir(rest)
		for dir := path.Dir(rest); ; dir = path.Dir(dir) {
			if hasConfig(filepath.Join(root, repo, filepath.FromSlash(dir))) {
				prefix = dir
			}
			if dir == "." {
				break
			}
		}
		byRepo[repo] = append(byRepo[repo], prefix)
	}
	for repo, ps := range byRepo {
		sort.Strings(ps)
		var kept []string
		for _, p := range ps {
			covered := false
			for _, k := range kept {
				if k == "." || p == k || strings.HasPrefix(p, k+"/") {
					covered = true
				}
			}
			if !covered {
				kept = append(kept, p)
			}
		}
		byRepo[repo] = kept
	}
	return byRepo, nil
}

func hasConfig(dir string) bool {
	for _, n := range []string{"deno.json", "deno.jsonc"} {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func gitOut(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func pathspecs(prefixes []string) []string {
	return append([]string{"--"}, prefixes...)
}

func stageRepos(ctx context.Context, o BuildOptions, root, src string, prefixes map[string][]string) ([]tsbundle.RepoRecord, int64, error) {
	names := make([]string, 0, len(prefixes))
	for r := range prefixes {
		names = append(names, r)
	}
	sort.Strings(names)
	var recs []tsbundle.RepoRecord
	var epoch int64
	var dirtyErrs []string
	for _, name := range names {
		dir := filepath.Join(root, name)
		ps := prefixes[name]
		head, err := gitOut(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return nil, 0, err
		}
		ct, err := gitOut(ctx, dir, "log", "-1", "--format=%ct", "HEAD")
		if err != nil {
			return nil, 0, err
		}
		if t, err := strconv.ParseInt(strings.TrimSpace(string(ct)), 10, 64); err == nil && t > epoch {
			epoch = t
		}
		status, err := gitOut(ctx, dir, append([]string{"status", "--porcelain=v1", "--untracked-files=no"}, pathspecs(ps)...)...)
		if err != nil {
			return nil, 0, err
		}
		dirty := strings.TrimSpace(string(status)) != ""
		if dirty && o.Release {
			var paths []string
			for _, l := range strings.Split(strings.TrimSpace(string(status)), "\n") {
				if len(l) > 3 {
					paths = append(paths, l[3:])
				}
			}
			dirtyErrs = append(dirtyErrs, fmt.Sprintf("%s is dirty in its staged paths (%s)", name, strings.Join(paths, ", ")))
		}
		recs = append(recs, tsbundle.RepoRecord{Name: name, Commit: strings.TrimSpace(string(head)), Dirty: dirty, Prefixes: ps})
		listing, err := gitOut(ctx, dir, append([]string{"ls-files", "-s", "-z"}, pathspecs(ps)...)...)
		if err != nil {
			return nil, 0, err
		}
		for _, rec := range strings.Split(string(listing), "\x00") {
			if rec == "" {
				continue
			}
			meta, p, ok := strings.Cut(rec, "\t")
			if !ok {
				return nil, 0, fmt.Errorf("tsbundle: git ls-files %s: %q", name, rec)
			}
			mode, _, _ := strings.Cut(meta, " ")
			if err := stageFile(filepath.Join(dir, filepath.FromSlash(p)), filepath.Join(src, name, filepath.FromSlash(p)), mode, o.Release); err != nil {
				return nil, 0, fmt.Errorf("tsbundle: staging %s/%s: %w", name, p, err)
			}
		}
	}
	if len(dirtyErrs) > 0 {
		return nil, 0, fmt.Errorf("tsbundle: a release bundles committed trees only: %s", strings.Join(dirtyErrs, "; "))
	}
	return recs, epoch, nil
}

func stageFile(from, to, mode string, release bool) error {
	if mode == "160000" {
		return nil
	}
	info, err := os.Lstat(from)
	if errors.Is(err, fs.ErrNotExist) && !release {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	switch {
	case mode == "120000" || info.Mode()&fs.ModeSymlink != 0:
		t, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(t, to)
	case mode == "100755":
		return tsbundle.CopyFile(from, to, 0o755)
	default:
		return tsbundle.CopyFile(from, to, 0o644)
	}
}

func snapshotLocks(src string, prefixes map[string][]string) ([]string, map[string][]byte, error) {
	var locks []string
	before := map[string][]byte{}
	for repo, ps := range prefixes {
		for _, p := range ps {
			dir := filepath.Join(src, repo, filepath.FromSlash(p))
			if !hasConfig(dir) {
				continue
			}
			rel := path.Join(repo, p, "deno.lock")
			locks = append(locks, rel)
			b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
			if err == nil {
				before[rel] = b
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, nil, err
			}
		}
	}
	sort.Strings(locks)
	return locks, before, nil
}

func fillCache(ctx context.Context, o BuildOptions, src string, fill denorun.Spec) error {
	env := []string{"HOME=" + os.Getenv("HOME")}
	opts := denorun.InstallOptions{Frozen: true, AllowImport: o.AllowImport}
	for _, e := range o.Entries {
		s := fill
		s.Dir, s.Entry = e.At(src)
		out, err := s.Install(ctx, env, opts)
		if err == nil {
			continue
		}
		if !strings.Contains(strings.ToLower(string(out)), "lockfile is out of date") {
			return fmt.Errorf("tsbundle: entry %s: %w", e.Name, err)
		}
		o.logf("entry %s: lockfile out of date; refreshing it in the staged copy only", e.Name)
		thaw := opts
		thaw.Frozen = false
		if _, err := s.Install(ctx, env, thaw); err != nil {
			return fmt.Errorf("tsbundle: entry %s: %w", e.Name, err)
		}
	}
	for _, e := range o.Entries {
		s := fill
		s.Dir, s.Entry = e.At(src)
		if _, err := s.Install(ctx, env, opts); err != nil {
			return fmt.Errorf("tsbundle: entry %s is not frozen after the fill: %w", e.Name, err)
		}
	}
	return nil
}

type lockFile struct {
	JSR    map[string]json.RawMessage `json:"jsr"`
	NPM    map[string]json.RawMessage `json:"npm"`
	Remote map[string]string          `json:"remote"`
}

type lockPins struct {
	jsr map[string]map[string]bool
	npm map[string]map[string]bool
}

func splitNameVersion(key string) (string, string, bool) {
	i := strings.LastIndex(key, "@")
	if i <= 0 {
		return "", "", false
	}
	v, _, _ := strings.Cut(key[i+1:], "_")
	return key[:i], v, true
}

func classifyLocks(src string, locks []string, before map[string][]byte) ([]tsbundle.LockRecord, lockPins, error) {
	pins := lockPins{jsr: map[string]map[string]bool{}, npm: map[string]map[string]bool{}}
	recs := []tsbundle.LockRecord{}
	for _, rel := range locks {
		now, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, pins, err
		}
		old, had := before[rel]
		state := LockFrozen
		switch {
		case !had:
			state = LockCreated
		case !bytes.Equal(old, now):
			state = LockDrifted
			var a, b map[string]any
			if json.Unmarshal(old, &a) == nil && json.Unmarshal(now, &b) == nil {
				delete(a, "workspace")
				delete(b, "workspace")
				if reflect.DeepEqual(a, b) {
					state = LockWorkspace
				}
			}
		}
		recs = append(recs, tsbundle.LockRecord{Path: rel, State: state})
		var lf lockFile
		if err := json.Unmarshal(now, &lf); err != nil {
			return nil, pins, fmt.Errorf("tsbundle: %s: %w", rel, err)
		}
		for k := range lf.JSR {
			if n, v, ok := splitNameVersion(k); ok {
				addPin(pins.jsr, n, v)
			}
		}
		for k := range lf.NPM {
			if n, v, ok := splitNameVersion(k); ok {
				addPin(pins.npm, n, v)
			}
		}
	}
	return recs, pins, nil
}

func addPin(m map[string]map[string]bool, name, version string) {
	if m[name] == nil {
		m[name] = map[string]bool{}
	}
	m[name][version] = true
}

func probe(ctx context.Context, o BuildOptions, work string, archive []byte, m tsbundle.Manifest) ([]tsbundle.EntryRecord, error) {
	bundle := filepath.Join(work, "probe-bundle")
	if err := WriteBundle(bundle, archive, m); err != nil {
		return nil, err
	}
	p, err := tsbundle.Embedded{FS: os.DirFS(bundle), Dir: "."}.Prepare(filepath.Join(work, "probe-state"))
	if err != nil {
		return nil, err
	}
	home := filepath.Join(work, "probe-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	recs := make([]tsbundle.EntryRecord, 0, len(o.Entries))
	for _, e := range o.Entries {
		rec := tsbundle.EntryRecord{EntrySpec: e}
		pctx, cancel := context.WithTimeout(ctx, o.ProbeTimeout)
		cmd, err := p.Spec(o.Deno, e, e.Probe).Command(pctx, []string{"HOME=" + home})
		if err != nil {
			cancel()
			return nil, err
		}
		if o.Offline {
			wrapped := exec.CommandContext(pctx, "unshare", append([]string{"-rn"}, cmd.Args...)...)
			wrapped.Dir, wrapped.Env = cmd.Dir, cmd.Env
			cmd = wrapped
		}
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			reason := lastLine(string(out))
			if strings.Contains(reason, "node_modules") {
				reason = "needs a node_modules directory: " + reason
			}
			rec.Reason = fmt.Sprintf("%v: %s", err, reason)
		} else {
			rec.OK = true
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "at ") {
			return l
		}
	}
	return ""
}
