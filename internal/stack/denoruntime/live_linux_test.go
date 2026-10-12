//go:build linux

package denoruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc/hostsvctest"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/install"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpapply"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/stack/denoruntime"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

const (
	tenant     = "denoruntime-tenant"
	settleFor  = 3 * time.Minute
	holdFor    = 8 * time.Second
	markerFile = "socialweb-computer-kcp/marker.txt"
)

var (
	gvrWorkspace = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	gvrDenoPod   = schema.GroupVersionResource{Group: "deno.computer", Version: "v1alpha1", Resource: "denopods"}
)

const podScript = `const root = Deno.env.get("SOCIALWEB_TS_ROOT") ?? "";
const marker = (await Deno.readTextFile(root + "/socialweb-computer-kcp/marker.txt")).trim();
console.log(JSON.stringify({ marker, key: Deno.env.get("SOCIALWEB_TS_KEY") }));
setInterval(() => {}, 3600000);
`

type upReport struct {
	Instance        string `json:"instance"`
	StateDir        string `json:"stateDir"`
	Socket          string `json:"socket"`
	AdminKubeconfig string `json:"adminKubeconfig"`
	Executables     map[string]struct {
		Path string `json:"path"`
	} `json:"executables"`
}

type stack struct {
	t     *testing.T
	repo  string
	dir   string
	bin   string
	state string
	id    string
	env   []string
	up    upReport
	base  *rest.Config
}

func liveExe(env, name string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	abs, _ := filepath.Abs(p)
	return abs
}

func orgRoot(t *testing.T, marker string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "org-"+marker)
	for _, r := range tsbundle.SiblingRepos {
		if err := os.MkdirAll(filepath.Join(d, r), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, markerFile), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func closureKey(t *testing.T, root string) string {
	t.Helper()
	k, err := denorun.KeyForPath(root)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (s *stack) build(name string, flags ...string) string {
	s.t.Helper()
	out := filepath.Join(s.dir, name)
	cmd := exec.Command("go", append(append([]string{"build", "-o", out}, flags...), "./cmd/socialweb-computer")...)
	cmd.Dir = s.repo
	if b, err := cmd.CombinedOutput(); err != nil {
		s.t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func openStack(t *testing.T, org string) *stack {
	t.Helper()
	livegate.Short(t, "this test runs real kcp, kine and deno processes under devsup and a core that runs the deno runtime")
	exes := map[string]string{"SOCIALWEB_KCP_PATH": liveExe("SOCIALWEB_KCP_PATH", "kcp"), "SOCIALWEB_KINE_PATH": liveExe("SOCIALWEB_KINE_PATH", "kine"),
		"SOCIALWEB_DENO_PATH": liveExe("SOCIALWEB_DENO_PATH", "deno")}
	for env, p := range exes {
		if p == "" {
			livegate.RequireLocal(t, "no executable for %s here (that env or PATH)", env)
			return nil
		}
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		livegate.RequireLocal(t, "XDG_RUNTIME_DIR is unset")
		return nil
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{t: t, repo: repo, dir: t.TempDir(), id: "t" + hostsvctest.RandomKey() + "-dr"}
	s.bin = s.build("socialweb-computer")
	s.state = filepath.Join(s.dir, "state")
	for env, p := range exes {
		s.env = append(s.env, env+"="+p)
	}
	s.env = append(s.env, tsbundle.OrgRootEnv+"="+org)
	t.Cleanup(func() { s.runE(nil, "dev", "down", "--instance", s.id) })
	s.run(&s.up, "dev", "up", "--instance", s.id, "--profile", "minimal")
	s.base, err = kcpserver.FrontRestConfig(s.up.AdminKubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *stack) runE(out any, args ...string) error {
	cmd := exec.Command(s.bin, append(args, "--state-dir", s.state)...)
	cmd.Env = append(os.Environ(), s.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %v\n%s\n%s", args, err, stdout.String(), stderr.String())
	}
	if out != nil {
		if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
			return fmt.Errorf("%v: %v\n%s", args, err, stdout.String())
		}
	}
	return nil
}

func (s *stack) run(out any, args ...string) {
	s.t.Helper()
	if err := s.runE(out, args...); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stack) client(ws string) dynamic.Interface {
	s.t.Helper()
	cfg := rest.CopyConfig(s.base)
	cfg.Host = strings.TrimSuffix(cfg.Host, "/") + "/clusters/" + ws
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		s.t.Fatal(err)
	}
	return c
}

func (s *stack) logs() string {
	b, _ := os.ReadFile(filepath.Join(s.up.StateDir, "logs", "core.log"))
	if len(b) > 6000 {
		b = b[len(b)-6000:]
	}
	return string(b)
}

func (s *stack) eventually(what string, within time.Duration, cond func() (bool, string)) {
	s.t.Helper()
	deadline := time.Now().Add(within)
	last := ""
	for {
		ok, why := cond()
		if ok {
			return
		}
		last = why
		if time.Now().After(deadline) {
			s.t.Fatalf("%s did not hold within %s: %s\ncore log tail:\n%s", what, within, last, s.logs())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (s *stack) tenant() {
	s.t.Helper()
	ctx := context.Background()
	root := s.client(kcpserver.RootCluster).Resource(gvrWorkspace)
	ws := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace",
		"metadata": map[string]any{"name": tenant}, "spec": map[string]any{"type": map[string]any{"name": "denoruntime", "path": "root"}}}}
	ap, err := kcpapply.New(s.base, kcpapply.Options{})
	if err != nil {
		s.t.Fatal(err)
	}
	s.eventually("the tenant workspace is applied (the core applied the denoruntime WorkspaceType)", settleFor, func() (bool, string) {
		_, err := ap.ApplyObjects(ctx, kcpserver.RootCluster, []manifests.Object{{Path: "tenant.yaml", Unstructured: ws}})
		return err == nil, fmt.Sprint(err)
	})
	s.eventually("the tenant workspace is Ready", settleFor, func() (bool, string) {
		got, err := root.Get(ctx, tenant, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		status, _ := json.Marshal(got.Object["status"])
		return phase == "Ready", "status " + string(status)
	})
}

func (s *stack) pods() dynamic.ResourceInterface {
	return s.client(kcpserver.RootCluster + ":" + tenant).Resource(gvrDenoPod).Namespace("default")
}

func (s *stack) createPod(name string) {
	s.t.Helper()
	pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "deno.computer/v1alpha1", "kind": "DenoPod",
		"metadata": map[string]any{"name": name, "namespace": "default", "finalizers": []any{v1alpha1.FinalizerDenoPod}},
		"spec": map[string]any{"script": podScript, "restartPolicy": "Always",
			"permissions": map[string]any{"env": map[string]any{"allow": true}, "read": map[string]any{"allow": true}, "noPrompt": true}}}}
	s.eventually("DenoPod "+name+" is created (the tenant's denoruntime binding)", settleFor, func() (bool, string) {
		_, err := s.pods().Create(context.Background(), pod, metav1.CreateOptions{})
		return err == nil || apierrors.IsAlreadyExists(err), fmt.Sprint(err)
	})
}

func (s *stack) deletePod(name string) {
	s.t.Helper()
	if err := s.pods().Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		s.t.Fatal(err)
	}
	s.eventually("DenoPod "+name+" is gone", settleFor, func() (bool, string) {
		_, err := s.pods().Get(context.Background(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprint(err)
	})
}

type podState struct {
	Phase    string
	RunID    string
	Restarts int64
}

func (s *stack) podStates() (map[string]podState, error) {
	list, err := s.pods().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string]podState{}
	for _, it := range list.Items {
		phase, _, _ := unstructured.NestedString(it.Object, "status", "phase")
		runID, _, _ := unstructured.NestedString(it.Object, "status", "runID")
		restarts, _, _ := unstructured.NestedInt64(it.Object, "status", "restarts")
		out[it.GetName()] = podState{Phase: phase, RunID: runID, Restarts: restarts}
	}
	return out, nil
}

type denoProc struct {
	PID   int
	RunID string
	Exe   string
	Env   map[string]string
}

func procEnv(pid int) map[string]string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, kv := range bytes.Split(b, []byte{0}) {
		k, v, ok := strings.Cut(string(kv), "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func denoProcs(runsDir string) []denoProc {
	entries, _ := os.ReadDir("/proc")
	var out []denoProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err != nil || filepath.Dir(cwd) != runsDir {
			continue
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil {
			if i := bytes.LastIndexByte(stat, ')'); i > 0 && len(stat) > i+2 && stat[i+2] == 'Z' {
				continue
			}
		}
		exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		out = append(out, denoProc{PID: pid, RunID: filepath.Base(cwd), Exe: exe, Env: procEnv(pid)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func (s *stack) runsDir() string { return denoruntime.RunsDir(s.up.StateDir) }

type settled struct {
	States map[string]podState
	Procs  []denoProc
}

func (s *stack) settle(names []string, key, marker string, before map[string]podState) settled {
	s.t.Helper()
	var got settled
	check := func() (bool, string) {
		states, err := s.podStates()
		if err != nil {
			return false, err.Error()
		}
		var listed []string
		for n := range states {
			listed = append(listed, n)
		}
		sort.Strings(listed)
		want := slices.Clone(names)
		sort.Strings(want)
		if !slices.Equal(listed, want) {
			return false, fmt.Sprintf("DenoPods %v, want exactly %v (a deleted DenoPod never returns)", listed, want)
		}
		procs := denoProcs(s.runsDir())
		byRun := map[string][]denoProc{}
		for _, p := range procs {
			byRun[p.RunID] = append(byRun[p.RunID], p)
		}
		for _, n := range names {
			st := states[n]
			if st.Phase != "Running" || st.RunID == "" {
				return false, fmt.Sprintf("%s is %+v", n, st)
			}
			if b, ok := before[n]; ok && (st.Restarts != b.Restarts+1 || st.RunID == b.RunID) {
				return false, fmt.Sprintf("%s restarts %d run %s, before %d run %s: want one restart onto a new run", n, st.Restarts, st.RunID, b.Restarts, b.RunID)
			}
			if len(byRun[st.RunID]) != 1 {
				return false, fmt.Sprintf("%s run %s has %d deno processes", n, st.RunID, len(byRun[st.RunID]))
			}
			p := byRun[st.RunID][0]
			if p.Env[denoruntime.TSKeyEnv] != key {
				return false, fmt.Sprintf("%s pid %d runs closure %q, want %q", n, p.PID, p.Env[denoruntime.TSKeyEnv], key)
			}
			out, _ := os.ReadFile(filepath.Join(s.runsDir(), st.RunID, "stdout.txt"))
			if !strings.Contains(string(out), `"marker":"`+marker+`"`) {
				return false, fmt.Sprintf("%s logged %q, want marker %s", n, out, marker)
			}
		}
		if len(procs) != len(names) {
			return false, fmt.Sprintf("%d deno processes under %s for %d DenoPods: runs %v", len(procs), s.runsDir(), len(names), runIDs(procs))
		}
		got = settled{States: states, Procs: procs}
		return true, ""
	}
	s.eventually("every DenoPod Running on closure "+marker+" with one deno process each", settleFor, check)
	first := got
	time.Sleep(holdFor)
	s.eventually("the settled state holding", time.Second, check)
	for n, st := range got.States {
		if first.States[n] != st {
			s.t.Fatalf("%s moved from %+v to %+v after settling: a DenoPod is restarted exactly once", n, first.States[n], st)
		}
	}
	return got
}

func fileSHA256(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func pidsGone(pids []int) (bool, string) {
	for _, pid := range pids {
		if syscall.Kill(pid, 0) == nil {
			return false, fmt.Sprintf("pid %d of the old core is alive", pid)
		}
	}
	return true, ""
}

func runIDs(ps []denoProc) []string {
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s(pid %d)", p.RunID, p.PID))
	}
	return out
}

func pidsOf(ps []denoProc) []int {
	var out []int
	for _, p := range ps {
		out = append(out, p.PID)
	}
	return out
}

func (s *stack) corePID() int {
	var c devinstance.Core
	if err := devinstance.ReadJSON(filepath.Join(s.up.StateDir, "core.json"), &c); err != nil {
		s.t.Fatal(err)
	}
	return c.PID
}

func TestADenoPodAppliedToTheKcpRunsOnTheEmbeddedDeno(t *testing.T) {
	org := orgRoot(t, "A")
	s := openStack(t, org)
	if s == nil {
		return
	}
	s.tenant()
	s.createPod("svc")
	got := s.settle([]string{"svc"}, closureKey(t, org), "A", nil)
	p := got.Procs[0]
	deno, err := filepath.EvalSymlinks(s.up.Executables["deno"].Path)
	if err != nil {
		t.Fatalf("the instance records no deno: %v", err)
	}
	if p.Exe != deno {
		t.Fatalf("the DenoPod runs %s, want the core's deno %s", p.Exe, deno)
	}
	var rec devinstance.Record
	if err := devinstance.ReadJSON(filepath.Join(s.up.StateDir, "instance.json"), &rec); err != nil {
		t.Fatal(err)
	}
	if unit := p.Env["SWC_UNIT"]; unit != rec.Layout.CoreUnit() {
		t.Fatalf("the deno process names unit %q, want the core unit %s", unit, rec.Layout.CoreUnit())
	}
	t.Logf("reading %s: DenoPod svc run %s pid %d exe %s closure %s", time.Now().UTC().Format(time.RFC3339), got.States["svc"].RunID, p.PID, p.Exe, p.Env[denoruntime.TSKeyEnv])
}

func TestAKilledCoreLeavesNoDenoProcessOnTheProcBackend(t *testing.T) {
	org := orgRoot(t, "A")
	s := openStack(t, org)
	if s == nil {
		return
	}
	key := closureKey(t, org)
	s.tenant()
	names := []string{"svc-1", "svc-2", "svc-3"}
	for _, n := range names {
		s.createPod(n)
	}
	got := s.settle(names, key, "A", nil)

	s.deletePod("svc-1")
	names = names[1:]
	got = s.settle(names, key, "A", nil)
	old := pidsOf(got.Procs)
	core := s.corePID()
	if err := syscall.Kill(core, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	s.eventually("no deno process of the killed core", time.Minute, func() (bool, string) { return pidsGone(old) })
	got = s.settle(names, key, "A", got.States)
	t.Logf("reading %s: kill -9 of core pid %d: old deno pids %v gone, new %v", time.Now().UTC().Format(time.RFC3339), core, old, pidsOf(got.Procs))

	s.deletePod("svc-2")
	names = names[1:]
	got = s.settle(names, key, "A", nil)
	old = pidsOf(got.Procs)
	s.run(nil, "dev", "restart", "core", "--instance", s.id)
	s.eventually("no deno process of the replaced core", time.Minute, func() (bool, string) { return pidsGone(old) })
	got = s.settle(names, key, "A", got.States)
	t.Logf("reading %s: dev restart core: old deno pids %v gone, new %v", time.Now().UTC().Format(time.RFC3339), old, pidsOf(got.Procs))
}

func TestAnUpgradeRestartsEveryRecordedDenoPodOnTheNewClosureExactlyOnce(t *testing.T) {
	livegate.Short(t, "this test places the core as a transient unit in this account's systemd user manager")
	if _, err := os.Stat(hostsvc.PrivateSocketPath()); err != nil {
		livegate.RequireLocal(t, "the user manager's private socket %s is missing: %v", hostsvc.PrivateSocketPath(), err)
		return
	}
	orgA, orgB := orgRoot(t, "A"), orgRoot(t, "B")
	s := openStack(t, orgA)
	if s == nil {
		return
	}
	keyA, keyB := closureKey(t, orgA), closureKey(t, orgB)
	if keyA == keyB {
		t.Fatal("the two closures share a key")
	}
	binB := s.build("socialweb-computer-b", "-trimpath")
	account := filepath.Join(s.dir, "account")
	current := filepath.Join(account, "bin", "current", install.CoreName)
	upgrade := func(bin string) {
		t.Helper()
		sum, err := fileSHA256(bin)
		if err != nil {
			t.Fatal(err)
		}
		err = install.Upgrade(context.Background(), install.UpgradeOptions{StateDir: account, Home: filepath.Join(s.dir, "home"), Binary: bin, SHA256: sum,
			SelfCheck: func(ctx context.Context, exe string, _ hostcontract.Role, _ string) error {
				return exec.CommandContext(ctx, exe, "version", "--json").Run()
			},
			Restart: func(context.Context, []string) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	key := hostsvctest.RandomKey()
	prefix := "socialweb-test-" + key + "-"
	slice := "socialweb-test" + key + ".slice"
	unit := prefix + "core.service"
	sd, err := hostsvc.Open(ctx, hostsvc.Config{Backend: hostsvc.BackendSystemdUser, UnitPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sd.Stop(ctx, unit)
		sd.ResetFailed(ctx, unit)
		sd.StopSlice(ctx, slice)
		sd.Close()
	})
	spec := hostsvc.UnitSpec{Name: unit, Description: "denoruntime test core " + s.id,
		Exec:    []string{current, "internal", "dev-core", "--instance-dir", s.up.StateDir},
		Env:     []string{"SWC_UNIT=" + unit},
		WorkDir: s.up.StateDir, Slice: slice, Restart: hostsvc.RestartAlways, KillSignal: syscall.SIGTERM, StopTimeout: 20 * time.Second,
		Output: hostsvc.Output{StdoutFile: filepath.Join(s.up.StateDir, "logs", "core.log"), StderrFile: filepath.Join(s.up.StateDir, "logs", "core.log")}}
	recordOrgRoot := func(org string) {
		t.Helper()
		var rec devinstance.Record
		if err := devinstance.ReadJSON(filepath.Join(s.up.StateDir, "instance.json"), &rec); err != nil {
			t.Fatal(err)
		}
		rec.OrgRoot = org
		if err := devinstance.WriteJSON(filepath.Join(s.up.StateDir, "instance.json"), rec); err != nil {
			t.Fatal(err)
		}
	}

	s.tenant()
	names := []string{"svc-1", "svc-2", "svc-3", "svc-4"}
	for _, n := range names {
		s.createPod(n)
	}
	got := s.settle(names, keyA, "A", nil)

	var rec devinstance.Record
	if err := devinstance.ReadJSON(filepath.Join(s.up.StateDir, "instance.json"), &rec); err != nil {
		t.Fatal(err)
	}
	proc, err := hostsvc.Open(ctx, hostsvc.Config{Backend: hostsvc.BackendProc, ProcSocket: s.up.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	old := pidsOf(got.Procs)
	if err := proc.Stop(ctx, rec.Layout.CoreUnit()); err != nil {
		t.Fatal(err)
	}
	s.eventually("no deno process of the devsup core", time.Minute, func() (bool, string) { return pidsGone(old) })
	upgrade(s.bin)
	if _, err := sd.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	got = s.settle(names, keyA, "A", got.States)

	cases := []struct {
		name    string
		key     string
		marker  string
		trigger func()
	}{
		{"systemctl --user restart", keyA, "A", func() {
			if b, err := exec.Command("systemctl", "--user", "restart", unit).CombinedOutput(); err != nil {
				t.Fatalf("systemctl --user restart %s: %v %s", unit, err, b)
			}
		}},
		{"kill -9 of the main process", keyA, "A", func() {
			st, err := sd.Inspect(ctx, unit)
			if err != nil || st.MainPID <= 0 {
				t.Fatalf("inspect %s: %+v %v", unit, st, err)
			}
			if err := syscall.Kill(st.MainPID, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
		}},
		{"upgrade to a build with a different closure", keyB, "B", func() {
			upgrade(binB)
			if err := sd.Stop(ctx, unit); err != nil {
				t.Fatal(err)
			}
			sd.ResetFailed(ctx, unit)
			recordOrgRoot(orgB)
			if _, err := sd.Start(ctx, spec); err != nil {
				t.Fatal(err)
			}
		}},
	}
	curKey, curMarker := keyA, "A"
	for _, c := range cases {
		gone := names[0]
		s.deletePod(gone)
		names = names[1:]
		got = s.settle(names, curKey, curMarker, nil)
		old := pidsOf(got.Procs)
		c.trigger()
		s.eventually("no deno process of the old core after "+c.name, time.Minute, func() (bool, string) { return pidsGone(old) })
		got = s.settle(names, c.key, c.marker, got.States)
		curKey, curMarker = c.key, c.marker
		t.Logf("reading %s: %s: deleted %s stayed gone, old deno pids %v gone, new %v on closure %s", time.Now().UTC().Format(time.RFC3339),
			c.name, gone, old, pidsOf(got.Procs), c.marker)
	}
}
