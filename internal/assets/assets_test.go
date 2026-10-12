package assets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

const helperEnv = "SOCIALWEB_ASSETS_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "sleep" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fixture struct {
	pin  versions.Pin
	blob []byte
}

func newFixture(t *testing.T, body string) fixture {
	t.Helper()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "usr", "bin", "tool"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "usr", "bin", "data"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tool", filepath.Join(src, "usr", "bin", "tool-link")); err != nil {
		t.Fatal(err)
	}
	digest, err := TreeDigest(src)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTar(zw, src); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	pin := versions.Pin{Name: "tool", Platform: "test/test", Version: "v1",
		Executables: map[string]string{"tool": "usr/bin/tool"}, LibraryPath: "usr/lib",
		AssetPlatform: versions.AssetPlatform{TreeSHA256: digest}}
	return fixture{pin: pin, blob: buf.Bytes()}
}

func (f fixture) source(opens *int) BlobSource {
	var mu sync.Mutex
	return func(name string) (io.ReadCloser, error) {
		if name != f.pin.Name {
			return nil, os.ErrNotExist
		}
		mu.Lock()
		if opens != nil {
			*opens++
		}
		mu.Unlock()
		return io.NopCloser(bytes.NewReader(f.blob)), nil
	}
}

func TestAnExtractedAssetThatNoLongerMatchesItsPinIsReplacedNotRun(t *testing.T) {
	f := newFixture(t, "#!/bin/sh\necho pinned\n")
	s := Store{Root: t.TempDir()}
	first, err := s.Resolve(f.pin, f.source(nil), "tool")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(s.Root, f.pin.TreeSHA256, "usr", "bin", "tool"); first.Path != want {
		t.Fatalf("path = %s, want the content-addressed %s", first.Path, want)
	}
	if first.Env[0] != "LD_LIBRARY_PATH="+filepath.Join(s.Root, f.pin.TreeSHA256, "usr", "lib") {
		t.Fatalf("env = %v", first.Env)
	}
	tampers := map[string]func(dir string){
		"bytes changed": func(dir string) {
			p := filepath.Join(dir, "usr", "bin", "tool")
			os.Chmod(p, 0o755)
			os.WriteFile(p, []byte("#!/bin/sh\necho tampered\n"), 0o755)
		},
		"file added": func(dir string) {
			os.WriteFile(filepath.Join(dir, "usr", "bin", "extra"), []byte("x"), 0o755)
		},
		"file removed": func(dir string) {
			os.Remove(filepath.Join(dir, "usr", "bin", "data"))
		},
		"mode changed": func(dir string) {
			os.Chmod(filepath.Join(dir, "usr", "bin", "data"), 0o755)
		},
		"symlink retargeted": func(dir string) {
			l := filepath.Join(dir, "usr", "bin", "tool-link")
			os.Remove(l)
			os.Symlink("data", l)
		},
	}
	for what, tamper := range tampers {
		t.Run(what, func(t *testing.T) {
			dir := filepath.Join(s.Root, f.pin.TreeSHA256)
			tamper(dir)
			if got, _ := TreeDigest(dir); got == f.pin.TreeSHA256 {
				t.Fatalf("the tamper did not change the tree")
			}
			again, err := s.Resolve(f.pin, f.source(nil), "tool")
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(again.Path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != "#!/bin/sh\necho pinned\n" {
				t.Fatalf("resolved %s holds %q: the mismatched extraction was handed out instead of replaced", again.Path, b)
			}
			if got, _ := TreeDigest(dir); got != f.pin.TreeSHA256 {
				t.Fatalf("after resolve the tree is %s, want the pin %s", got, f.pin.TreeSHA256)
			}
		})
	}
	var left []string
	scratch, _ := filepath.Glob(filepath.Join(s.Root, ".*"))
	for _, p := range scratch {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			left = append(left, p)
		}
	}
	if len(left) != 0 {
		t.Fatalf("scratch dirs left behind: %v", left)
	}
}

func TestABlobThatDoesNotUnpackToItsPinIsRefused(t *testing.T) {
	f := newFixture(t, "#!/bin/sh\necho pinned\n")
	other := newFixture(t, "#!/bin/sh\necho other\n")
	s := Store{Root: t.TempDir()}
	_, err := s.Resolve(f.pin, other.source(nil), "tool")
	if err == nil || !strings.Contains(err.Error(), "pins") {
		t.Fatalf("err = %v, want a refusal naming the pin", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, f.pin.TreeSHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a blob that does not match its pin was placed: %v", err)
	}
}

func TestAReleaseBuildWithAMissingAssetRefusesToStart(t *testing.T) {
	pins := []versions.Pin{{Name: "kcp"}, {Name: "kine"}, {Name: "postgres"}}
	fsys := fstest.MapFS{
		"blobs/linux_amd64/kcp.tar.zst":      {Data: []byte("x")},
		"blobs/linux_amd64/postgres.tar.zst": {Data: []byte{}},
		"blobs/linux_amd64/placeholder":      {Data: []byte{}},
	}
	got := MissingBlobs(pins, fsys, "blobs/linux_amd64")
	if strings.Join(got, ",") != "kine,postgres" {
		t.Fatalf("missing = %v, want kine (absent) and postgres (empty)", got)
	}
	s := Store{Root: t.TempDir()}
	f := newFixture(t, "x")
	_, err := s.Resolve(f.pin, FSBlobs(fstest.MapFS{}, "blobs/linux_amd64"), "tool")
	if err == nil || !strings.Contains(err.Error(), "missing from this build") {
		t.Fatalf("err = %v, want the missing blob named", err)
	}
	if !Release() {
		if err := Ready(); err != nil {
			t.Fatalf("a dev build has no embedded set to miss, Ready = %v", err)
		}
	}
}

func TestTwoDevInstancesShareOneExtractionOfAnAsset(t *testing.T) {
	home := t.TempDir()
	env := mapEnv{"XDG_CACHE_HOME": filepath.Join(home, "cache")}
	root, err := DevCache(env)
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(home, "cache", "socialweb-computer", "runtime") {
		t.Fatalf("dev cache = %s", root)
	}
	f := newFixture(t, "#!/bin/sh\necho shared\n")
	opens := 0
	src := f.source(&opens)
	var wg sync.WaitGroup
	paths := make([]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := Store{Root: root}.Resolve(f.pin, src, "tool")
			paths[i], errs[i] = e.Path, err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if paths[0] != paths[1] {
		t.Fatalf("two instances resolved %s and %s; the per-user cache must hold one extraction", paths[0], paths[1])
	}
	before := opens
	st1, _ := os.Stat(paths[0])
	e, err := Store{Root: root}.Resolve(f.pin, src, "tool")
	if err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(e.Path)
	if opens != before || !os.SameFile(st1, st2) {
		t.Fatalf("a verified extraction was unpacked again (opens %d -> %d)", before, opens)
	}
	var dirs []os.DirEntry
	all, _ := os.ReadDir(root)
	for _, d := range all {
		if d.IsDir() {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) != 1 || dirs[0].Name() != f.pin.TreeSHA256 {
		var names []string
		for _, d := range dirs {
			names = append(names, d.Name())
		}
		t.Fatalf("cache holds %v, want exactly %s", names, f.pin.TreeSHA256)
	}
}

type mapEnv map[string]string

func (m mapEnv) LookupEnv(k string) (string, bool) {
	v, ok := m[k]
	return v, ok
}

func oldDir(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(d, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "bin", "x"), []byte("x"), 0o555)
	ts := time.Now().Add(-age)
	os.Chtimes(d, ts, ts)
	return d
}

func TestARuntimeDirNamedByALoadedUnitIsNeverCollected(t *testing.T) {
	root := t.TempDir()
	h := func(c byte) string { return strings.Repeat(string(c), 64) }
	execDir := oldDir(t, root, h('a'), 72*time.Hour)
	envDir := oldDir(t, root, h('b'), 72*time.Hour)
	current := oldDir(t, root, h('c'), 72*time.Hour)
	recent := oldDir(t, root, h('d'), time.Minute)
	orphan := oldDir(t, root, h('e'), 72*time.Hour)
	staleTmp := oldDir(t, root, ".tmp-0123", 72*time.Hour)
	systemdUser := UnitListerFunc(func(ctx context.Context) ([]UnitRef, error) {
		return []UnitRef{{Backend: "systemd-user", Name: "socialweb-kcp-a.service",
			Exec: []string{filepath.Join(execDir, "bin", "x"), "--flag"}}}, nil
	})
	proc := UnitListerFunc(func(ctx context.Context) ([]UnitRef, error) {
		return []UnitRef{{Backend: "proc", Name: "socialweb-pg.service", Exec: []string{"/bin/true"},
			Env: []string{"LD_LIBRARY_PATH=/usr/lib:" + filepath.Join(envDir, "usr", "lib", "sys")}}}, nil
	})
	policy := GCPolicy{Keep: []string{h('c')}, Window: 24 * time.Hour}

	broken := UnitListerFunc(func(ctx context.Context) ([]UnitRef, error) { return nil, errors.New("bus unreachable") })
	if _, err := Collect(context.Background(), root, []UnitLister{systemdUser, broken}, policy); err == nil {
		t.Fatal("gc ran with an unreadable unit lister; it must refuse")
	}
	for _, d := range []string{execDir, envDir, current, recent, orphan, staleTmp} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s removed by a refused gc: %v", d, err)
		}
	}

	rep, err := Collect(context.Background(), root, []UnitLister{systemdUser, proc}, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{execDir, envDir, current, recent} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was collected: %v (report %+v)", d, err, rep)
		}
	}
	for _, d := range []string{orphan, staleTmp} {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is unreferenced and old but was kept (report %+v)", d, rep)
		}
	}
	if len(rep.Removed) != 2 {
		t.Fatalf("removed %v, want the orphan and the stale scratch dir", rep.Removed)
	}
}

func TestDevGcRefusesWhileADevsupOfThisUserRuns(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Args = []string{"/opt/fake/socialweb-computer", "internal", "devsup", "--state", "x"}
	cmd.Env = append(os.Environ(), helperEnv+"=sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var running []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running = ProcessesOfUID(os.Getuid(), IsDevsup)
		if contains(running, cmd.Process.Pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !contains(running, cmd.Process.Pid) && filepath.Separator == '/' && fileExists("/proc/self/cmdline") {
		t.Fatalf("the proc scan did not see devsup pid %d (saw %v)", cmd.Process.Pid, running)
	}
	root := t.TempDir()
	orphan := oldDir(t, root, strings.Repeat("f", 64), 72*time.Hour)
	_, err := DevCollect(context.Background(), root, []int{cmd.Process.Pid}, nil, GCPolicy{Window: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "devsup") {
		t.Fatalf("err = %v, want a refusal naming the running devsup", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("dev gc removed %s while a devsup ran", orphan)
	}
	rep, err := DevCollect(context.Background(), root, nil, nil, GCPolicy{Window: time.Hour})
	if err != nil || len(rep.Removed) != 1 {
		t.Fatalf("with no devsup running: %+v, %v", rep, err)
	}
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
