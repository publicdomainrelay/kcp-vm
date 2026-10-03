//go:build linux

package materialiser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/identitybundle"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portscan"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

const (
	helperEnv    = "MATERIALISER_HELPER"
	helperName   = "MATERIALISER_NAME"
	helperLog    = "MATERIALISER_LOG"
	helperListen = "MATERIALISER_LISTEN"
	readyLine    = "ready"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "" {
		os.Exit(m.Run())
	}
	name := os.Getenv(helperName)
	path := os.Getenv(helperLog)
	_ = logLine(path, name+"-started")
	if raw := os.Getenv(helperListen); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			os.Exit(2)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			os.Exit(3)
		}
		defer ln.Close()
		_ = logLine(path, name+"-bound")
	}
	fmt.Fprintln(os.Stdout, readyLine)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	<-ch
	_ = logLine(path, name+"-stopped")
	os.Exit(0)
}

func logLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

func readOrder(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read order log: %v", err)
	}
	return strings.Fields(string(raw))
}

func helperSpec(name, logPath string) processrunner.Spec {
	return processrunner.Spec{
		Argv: []string{os.Args[0], "-test.run=TestMain"},
		Env: append(os.Environ(),
			helperEnv+"=1", helperName+"="+name, helperLog+"="+logPath),
	}
}

func announced(name, logPath string) func(context.Context, *processrunner.Process) error {
	return func(ctx context.Context, p *processrunner.Process) error {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if readyAnnounced(p.Output()) {
				return logLine(logPath, name+"-ready")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("child never announced readiness; its output tail was %q", p.Output())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func readyAnnounced(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == readyLine {
			return true
		}
	}
	return false
}

func stepBuilder(logPath string) StepBuilder {
	return func(Observed, Leases) ([]procgroup.Step, error) {
		components := processComponents()
		steps := make([]procgroup.Step, 0, len(components))
		for _, c := range components {
			name := string(c)
			steps = append(steps, procgroup.Step{
				Name:  c,
				Spec:  helperSpec(name, logPath),
				Ready: announced(name, logPath),
			})
		}
		return steps, nil
	}
}

func processComponents() []procgroup.Component {
	out := make([]procgroup.Component, 0, len(procgroup.StackOrder))
	for _, c := range procgroup.StackOrder {
		if c == procgroup.ComponentPostgres {
			continue
		}
		out = append(out, c)
	}
	return out
}

func networks(t *testing.T) *subnetallocator.Allocator {
	t.Helper()
	n, err := subnetallocator.Open(subnetallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "networks.json"),
	})
	if err != nil {
		t.Fatalf("subnetallocator.Open: %v", err)
	}
	return n
}

func testOptions() procgroup.Options {
	return procgroup.Options{PostgresUp: func() bool { return true }}
}

func testBundle() *identitybundle.Bundle {
	b := &identitybundle.Bundle{Entries: make([]identitybundle.Entry, 0, len(identitybundle.Paths))}
	for i, p := range identitybundle.Paths {
		b.Entries = append(b.Entries, identitybundle.Entry{
			Path:    p,
			Bytes:   []byte(fmt.Sprintf("bundle-entry-%d-%s", i, p)),
			ModTime: time.Unix(1700000000, 0),
		})
	}
	return b
}

func writeBundle(t *testing.T, b *identitybundle.Bundle) string {
	t.Helper()
	dir := t.TempDir()
	for _, e := range b.Entries {
		full := filepath.Join(dir, e.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, e.Bytes, 0o600); err != nil {
			t.Fatalf("write %s: %v", e.Path, err)
		}
	}
	return dir
}

func tamperServiceKey(t *testing.T, dir string) {
	t.Helper()
	full := filepath.Join(dir, "tls/service.key")
	if err := os.WriteFile(full, []byte("a different service key"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
}

func guard(src *identitybundle.Bundle, dataDir, logPath string) IdentityGuard {
	return func(_ context.Context, _ Observed) error {
		_ = logLine(logPath, "verify")
		if err := src.Verify(dataDir); err != nil {
			_ = logLine(logPath, "verify-failed")
			return err
		}
		return nil
	}
}

type countingHost struct {
	*Orphans
	spawns int
	reaps  int
}

func (h *countingHost) Spawn(ctx context.Context, cluster string, steps []procgroup.Step) (*procgroup.Group, error) {
	h.spawns++
	return h.Orphans.Spawn(ctx, cluster, steps)
}

func (h *countingHost) Reap(ctx context.Context, cluster string) error {
	h.reaps++
	return h.Orphans.Reap(ctx, cluster)
}

type harness struct {
	t       *testing.T
	name    string
	logPath string
	dataDir string
	src     *identitybundle.Bundle
	ports   *portallocator.Allocator
	local   *LocalHost
	host    *countingHost
	rec     *Reconciler
	now     time.Time
	minted  int

	identityState v1alpha1.IdentityState
	saPresent     bool
}

func newHarness(t *testing.T, tampered bool) *harness {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "order")

	src := testBundle()
	dataDir := writeBundle(t, src)
	if tampered {
		tamperServiceKey(t, dataDir)
	}

	ports, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(dir, "ports.json"),
		Range:     portallocator.Range{First: 20000, Last: 20099},
		Ephemeral: &portallocator.Range{First: 49152, Last: 65535},
		Probe:     func(int) bool { return false },
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}

	local := NewLocalHost(testOptions())
	local.Grace = 300 * time.Millisecond
	host := &countingHost{Orphans: NewOrphans(local, map[string][]string{})}

	h := &harness{
		t: t, name: "web", logPath: logPath, dataDir: dataDir, src: src,
		ports: ports, local: local, host: host,
		now: time.Unix(1700000000, 0),
	}
	rec, err := New(Options{
		Host:        host,
		Ports:       ports,
		Networks:    networks(t),
		Steps:       stepBuilder(logPath),
		Identity:    guard(src, dataDir, logPath),
		NewInstance: func() string { h.minted++; return fmt.Sprintf("instance-%d", h.minted) },
		Now:         func() time.Time { return h.now },
		InitTimeout: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}
	h.rec = rec
	t.Cleanup(func() {
		_ = local.Reap(context.Background(), h.name)
	})
	return h
}

func (h *harness) cluster() v1alpha1.Cluster {
	return v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: h.name, Generation: 1},
		Spec:       v1alpha1.ClusterSpec{Account: "did:plc:tenant", Running: true},
	}
}

func apply(c v1alpha1.Cluster, res Result) v1alpha1.Cluster {
	c.Status.Phase = res.Phase
	if res.URL != "" {
		c.Status.URL = res.URL
	}
	if res.Instance != "" {
		c.Status.Instance = res.Instance
	}
	c.Status.Conditions = res.Conditions
	return c
}

func (h *harness) observe(c v1alpha1.Cluster, pathStatus int, reachable bool) Observed {
	h.now = h.now.Add(2 * time.Second)
	return Observed{
		Cluster:               c,
		PathStatus:            pathStatus,
		Reachable:             reachable,
		IdentityState:         h.identityState,
		ServiceAccountPresent: h.saPresent,
		Now:                   h.now,
	}
}

func readyPhase(t *testing.T) v1alpha1.ClusterPhase {
	t.Helper()
	settled, err := revocation.WakeSettled(revocation.Effect{State: v1alpha1.IdentityRevoked})
	if err != nil {
		t.Fatalf("WakeSettled(terminal): %v", err)
	}
	phase, err := revocation.Ready(settled)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return phase
}

func (h *harness) reconcile(c v1alpha1.Cluster, pathStatus int, reachable bool) Result {
	h.t.Helper()
	res, err := h.rec.Reconcile(context.Background(), h.observe(c, pathStatus, reachable))
	if err != nil {
		h.t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (h *harness) up() (v1alpha1.Cluster, Result, Result) {
	h.t.Helper()
	c := h.cluster()
	first := h.reconcile(c, 0, false)
	if first.Phase != v1alpha1.ClusterInitializing {
		h.t.Fatalf("first pass phase = %q, want Initializing", first.Phase)
	}
	c = apply(c, first)
	second := h.reconcile(c, 0, true)
	if second.Phase != readyPhase(h.t) {
		h.t.Fatalf("second pass phase = %q, want the settled wake phase", second.Phase)
	}
	return apply(c, second), first, second
}

func hasOps(got []Op, want ...Op) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

func opsString(ops []Op) string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = string(o)
	}
	return strings.Join(out, " -> ")
}

func indexOf(seq []string, want string) int {
	for i, s := range seq {
		if s == want {
			return i
		}
	}
	return -1
}

func TestThePostgresAnswerIsAskedPerCluster(t *testing.T) {
	up := map[string]bool{"ready": true, "notready": false}
	h := NewLocalHost(procgroup.Options{})
	h.PostgresUp = func(cluster string) bool { return up[cluster] }
	h.Grace = 300 * time.Millisecond

	logPath := filepath.Join(t.TempDir(), "order")
	steps := []procgroup.Step{{
		Name:  procgroup.ComponentOpenBao,
		Spec:  helperSpec("openbao", logPath),
		Ready: announced("openbao", logPath),
	}}

	ctx := context.Background()
	if _, err := h.Spawn(ctx, "notready", steps); !errors.Is(err, procgroup.ErrPostgresUnavailable) {
		t.Fatalf("Spawn(notready) = %v, want ErrPostgresUnavailable -- and note that a "+
			"FLAT predicate would have answered this one `true` from the other cluster's "+
			"state, which is the bug this hook exists to make impossible", err)
	}
	g, err := h.Spawn(ctx, "ready", steps)
	if err != nil {
		t.Fatalf("Spawn(ready) = %v, want the group to start", err)
	}
	if _, ok := g.Process(procgroup.ComponentOpenBao); !ok {
		t.Fatal("Spawn(ready) returned a group with no openbao handle")
	}
	if err := h.Reap(ctx, "ready"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
}

func TestBringUpReachesReadyInTheADROrder(t *testing.T) {
	h := newHarness(t, false)
	final, first, _ := h.up()

	if final.Status.URL == "" {
		t.Fatal("Ready with status.URL empty -- K2 makes an absent URL a hard stop that loops the reconcile forever")
	}
	api, ok := h.ports.PortFor(h.name, portallocator.PurposeK3sSecure)
	if !ok {
		t.Fatal("no apiserver lease: the URL would not name a listener this cluster holds")
	}
	wantURL := fmt.Sprintf("https://127.0.0.1:%d", api.Port)
	if final.Status.URL != wantURL {
		t.Fatalf("status.URL = %q, want %q -- the URL must embed the ALLOCATED apiserver port, "+
			"because that is the only port k3s binds. MEASURED: k3s binds *:L and 127.0.0.1:L+1 and "+
			"nothing on --advertise-port, and /cacerts answers 200 on L and 401 on P, so a URL "+
			"naming a port the apiserver does not bind is a URL a node loops on",
			final.Status.URL, wantURL)
	}
	if final.Status.Instance == "" {
		t.Fatal("status.instance was never minted; it is the only durable discriminator there is")
	}
	for _, p := range DefaultPurposes {
		if _, ok := h.ports.PortFor(h.name, p); !ok {
			t.Fatalf("no lease held for %s", p)
		}
	}
	if !hasOps(first.Ops, OpAllocatePorts, OpSpawn) {
		t.Fatalf("bring-up ops = %s", opsString(first.Ops))
	}

	got := readOrder(t, h.logPath)
	want := []string{
		"openbao-started", "openbao-ready",
		"kine-started", "kine-ready",
		"verify",
		"k3s-server-started", "k3s-server-ready",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("bring-up order =\n  %v\nwant\n  %v", got, want)
	}
	if v := indexOf(got, "verify"); v > indexOf(got, "k3s-server-started") {
		t.Fatal("the bundle verify ran after k3s started; k3s reconciles bootstrap data on start, so the verify must precede the START")
	}
}

func TestTheVaultIsNotInTheDefaultCarve(t *testing.T) {
	for _, p := range DefaultPurposes {
		if p == portallocator.PurposeOpenBao {
			t.Fatal("PurposeOpenBao is back in materialiser.DefaultPurposes. Every entry here is " +
				"leased for as long as a cluster's process group is, and the vault is not a " +
				"component of that group: nothing enables a KV mount, nothing writes a bundle into " +
				"a vault and nothing points a SecretStore at one, so the lease would be held for a " +
				"process that provides nothing. Measured: a fresh vault from the rendered config " +
				"answers GET /v1/sys/health 501 {\"initialized\":false,\"sealed\":true}, which the " +
				"readiness gate (200/429/473) refuses, so the cluster would park in Unavailable for " +
				"a reason that is not the cluster's -- and the identity bundle's real home today is " +
				"the file tree under <DataDir>/server/tls. Lease it explicitly for the stack that " +
				"consumes it (clusterprovider.PurposesFor of a Stack naming ComponentOpenBao) until " +
				"an unseal policy exists that is worth having")
		}
	}
}

func TestBundleVerifyHaltsAndNeverRetries(t *testing.T) {
	h := newHarness(t, true)
	c := h.cluster()

	res := h.reconcile(c, 0, false)
	if res.Phase != v1alpha1.ClusterUnavailable {
		t.Fatalf("phase = %q, want Unavailable", res.Phase)
	}
	if !res.Halted {
		t.Fatal("the reconcile did not halt on a tampered bundle")
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %s on a halt; a requeue IS the retry the ADR forbids", res.RequeueAfter)
	}
	if res.Alert == "" {
		t.Fatal("no alert: the halt is an alarm, and an alarm nobody is told about is a silent failure")
	}
	cond := findCondition(res.Conditions, v1alpha1.ConditionIdentityVerificationFailed)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("conditions[IdentityVerificationFailed] = %+v, want True", cond)
	}
	if !hasOps(res.Ops, OpIdentityHalted) {
		t.Fatalf("ops = %s", opsString(res.Ops))
	}

	order := readOrder(t, h.logPath)
	if indexOf(order, "k3s-server-started") >= 0 {
		t.Fatalf("k3s STARTED against an unverified bundle: %v", order)
	}
	if indexOf(order, "verify-failed") < 0 {
		t.Fatalf("the guard did not record a failure: %v", order)
	}
	if h.host.spawns != 1 {
		t.Fatalf("spawns = %d, want 1", h.host.spawns)
	}

	bad := apply(c, res)
	again := h.reconcile(bad, 0, false)
	if h.host.spawns != 1 {
		t.Fatalf("spawns = %d after a second pass; the halt retried", h.host.spawns)
	}
	if !hasOps(again.Ops, OpIdentityHalted) {
		t.Fatalf("second pass ops = %s", opsString(again.Ops))
	}
	if got := readOrder(t, h.logPath); strings.Join(got, " ") != strings.Join(order, " ") {
		t.Fatalf("the second pass touched the data dir again: %v", got)
	}

	results, err := h.rec.Startup(context.Background(), []Observed{h.observe(bad, 0, false)})
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Startup returned %d results for a halted cluster", len(results))
	}
	if h.host.spawns != 1 {
		t.Fatalf("Startup respawned a halted cluster (spawns = %d)", h.host.spawns)
	}
}

func TestStartupReapsRespawnsAndBumpsInstance(t *testing.T) {
	h := newHarness(t, false)
	up, _, _ := h.up()
	oldInstance := up.Status.Instance
	oldPids := h.local.PIDs(h.name)
	if len(oldPids) != len(processComponents()) {
		t.Fatalf("live children = %v, want one per STEP -- postgres is not among them, it is "+
			"the library's child and not this group's", oldPids)
	}

	fresh := NewLocalHost(testOptions())
	fresh.Grace = 300 * time.Millisecond
	survivors := map[string][]string{h.name: {}}
	for _, pid := range oldPids {
		survivors[h.name] = append(survivors[h.name], fmt.Sprint(pid))
	}
	orphans := NewOrphans(fresh, survivors)
	host := &countingHost{Orphans: orphans}
	rec, err := New(Options{
		Host:        host,
		Ports:       h.ports,
		Networks:    networks(t),
		Steps:       stepBuilder(h.logPath),
		Identity:    guard(h.src, h.dataDir, h.logPath),
		NewInstance: func() string { h.minted++; return fmt.Sprintf("instance-%d", h.minted) },
		Now:         func() time.Time { return h.now },
		InitTimeout: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Reap(context.Background(), h.name) })

	results, err := rec.Startup(context.Background(), []Observed{h.observe(up, 0, false)})
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Startup returned %d results, want 1", len(results))
	}
	res := results[0]

	if res.Instance == "" || res.Instance == oldInstance {
		t.Fatalf("status.instance = %q, was %q; the restart rule must BUMP it", res.Instance, oldInstance)
	}
	if !hasOps(res.Ops, OpReapOrphans, OpInstanceBumped, OpSpawn) {
		t.Fatalf("ops = %s, want the orphan reap, then the bump and a fresh spawn", opsString(res.Ops))
	}
	if i, j := indexOfStrings(res.Ops, OpReapOrphans), indexOfStrings(res.Ops, OpSpawn); i > j {
		t.Fatalf("the orphan reap came after the spawn: %s", opsString(res.Ops))
	}
	if i, j := indexOfStrings(res.Ops, OpReapOrphans), indexOfStrings(res.Ops, OpInstanceBumped); i > j {
		t.Fatalf("the instance was bumped before the survivors were reaped: %s -- the new "+
			"instance is the durable name of the token a group holds, and a pass that names a "+
			"token before it has taken the group that held the old one is describing a group "+
			"that is still running", opsString(res.Ops))
	}
	if still := orphans.Alive(h.name); len(still) != 0 {
		t.Fatalf("the reaped handles are still recorded as alive: %v -- the next pass would "+
			"kill them again, and once the kernel recycles one of those pids that kill lands on "+
			"a process that was never ours", still)
	}

	order := readOrder(t, h.logPath)
	for _, c := range processComponents() {
		if indexOf(order, string(c)+"-stopped") < 0 {
			t.Fatalf("%s was never stopped: %v", c, order)
		}
	}
	_ = h.local.Reap(context.Background(), h.name)
	for _, pid := range oldPids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("pid %d is still there (%v); an orphaned k3s is a live apiserver a new agent can join", pid, err)
		}
	}
	if h.local.Live(h.name) {
		t.Fatal("the old host still believes it holds a group")
	}
}

func TestOFFRetainsTheStoreAndDoesNotReleaseAPort(t *testing.T) {
	h := newHarness(t, false)
	up, _, _ := h.up()
	kine, _ := h.ports.PortFor(h.name, portallocator.PurposeKine)
	url, instance := up.Status.URL, up.Status.Instance

	off := up
	off.Spec.Running = false
	h.host.reaps = 0
	first := h.reconcile(off, 200, false)
	if first.Phase != v1alpha1.ClusterUnavailable {
		t.Fatalf("phase = %q, want Unavailable", first.Phase)
	}
	if !hasOps(first.Ops, OpShutPath, OpAwaitPathShut) {
		t.Fatalf("pass 1 ops = %s", opsString(first.Ops))
	}
	if h.host.reaps != 0 {
		t.Fatalf("reaped while the mounted path still answered %d -- that interval is the lost-write window", 200)
	}
	if first.RequeueAfter == 0 {
		t.Fatal("no requeue: nothing would ever reap this cluster")
	}
	if first.URL != url {
		t.Fatalf("pass 1 cleared status.URL (%q); OFF leaves it live", first.URL)
	}

	off = apply(off, first)
	second := h.reconcile(off, 503, false)
	if !hasOps(second.Ops, OpPathShut, OpReap) {
		t.Fatalf("pass 2 ops = %s", opsString(second.Ops))
	}
	if h.host.reaps != 1 {
		t.Fatalf("reaps = %d, want 1", h.host.reaps)
	}
	if second.URL != url || second.Instance != instance {
		t.Fatalf("status.URL/instance changed on OFF: %q/%q, want %q/%q -- the mount keeps resolving to the same forward", second.URL, second.Instance, url, instance)
	}

	still, ok := h.ports.PortFor(h.name, portallocator.PurposeKine)
	if !ok {
		t.Fatal("OFF released the kine lease: the next ON is a silent misroute")
	}
	if still.Port != kine.Port {
		t.Fatalf("the kine port moved on OFF: %d -> %d", kine.Port, still.Port)
	}
	for _, p := range DefaultPurposes {
		if _, ok := h.ports.PortFor(h.name, p); !ok {
			t.Fatalf("OFF released the %s lease", p)
		}
	}

	on := off
	on.Spec.Running = true
	back := h.reconcile(on, 0, false)
	if !hasOps(back.Ops, OpAllocatePorts, OpSpawn) {
		t.Fatalf("re-ON ops = %s", opsString(back.Ops))
	}
	rebound, _ := h.ports.PortFor(h.name, portallocator.PurposeKine)
	if rebound.Port != kine.Port {
		t.Fatalf("re-ON moved the kine port %d -> %d", kine.Port, rebound.Port)
	}
	if back.Instance != instance {
		t.Fatalf("re-ON minted a new instance (%q -> %q); a dead process with a live CR is a RE-BIND, not a rebuild", instance, back.Instance)
	}
}

func TestOnlyAnUnreachedPathIsShut(t *testing.T) {
	h := newHarness(t, false)
	up, _, _ := h.up()
	off := up
	off.Spec.Running = false
	h.host.reaps = 0

	for _, code := range []int{http.StatusOK, http.StatusNoContent, 299, 300, http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusGatewayTimeout} {
		res := h.reconcile(off, code, true)
		if !hasOps(res.Ops, OpShutPath, OpAwaitPathShut) {
			t.Fatalf("the mounted path answered %d and the pass did not hold: ops = %s. A code the "+
				"tenant's apiserver itself produced means the request WAS REACHED through the "+
				"mount, so the tenant's path is still being served and nothing may be destroyed "+
				"while it is", code, opsString(res.Ops))
		}
		if res.RemoveFinalizer {
			t.Fatalf("the mounted path answered %d and the finalizer was released", code)
		}
		if h.host.reaps != 0 {
			t.Fatalf("the mounted path answered %d and the group was still reaped: the mount "+
				"reaching the tenant's apiserver is the thing the gate waits for, and reaping over "+
				"it is the lost write the gate exists to prevent", code)
		}
	}

	for _, code := range []int{http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable} {
		res := h.reconcile(off, code, true)
		if !hasOps(res.Ops, OpPathShut, OpReap) {
			t.Fatalf("the mounted path answered %d and the pass did not reap: ops = %s. A code "+
				"that does not prove the tenant's apiserver was reached -- kcp refusing a "+
				"workspace, or a reverse proxy with no backend -- is a shut path, and staying shut "+
				"on one is stuck-and-billing for no reason", code, opsString(res.Ops))
		}
	}
	if h.host.reaps == 0 {
		t.Fatal("no code in the shut set ever reaped: the gate is vacuous in the other direction " +
			"and would hold a dead path for ever")
	}
}

func TestUnmeasuredCodesAreNotShut(t *testing.T) {
	for _, code := range []int{300, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusGatewayTimeout} {
		if shut(Observed{PathStatus: code}) {
			t.Errorf("PathStatus %d read shut, but only 404, 502 and 503 are measured shut answers; "+
				"every other non-zero code is unproven and the gate must hold, because a reap on a "+
				"guess is the lost write the gate exists to prevent", code)
		}
	}
}

func TestTeardownGatesThenReleasesInOrder(t *testing.T) {
	h := newHarness(t, false)
	up, _, _ := h.up()
	zero := int64(0)
	up.Status.NodeExpiry = &zero

	dying := up
	dying.DeletionTimestamp = &metav1.Time{Time: h.now}
	h.host.reaps = 0
	first := h.reconcile(dying, 200, true)
	if first.Phase != v1alpha1.ClusterDraining {
		t.Fatalf("phase = %q, want Draining", first.Phase)
	}
	if first.RemoveFinalizer {
		t.Fatal("the finalizer was released while the tenant path still answered 200")
	}
	if h.host.reaps != 0 {
		t.Fatal("the process group was reaped before the gate passed")
	}
	if _, ok := h.ports.PortFor(h.name, portallocator.PurposeKine); !ok {
		t.Fatal("ports were released before the gate passed")
	}

	dying = apply(dying, first)
	dying.DeletionTimestamp = &metav1.Time{Time: h.now}
	second := h.reconcile(dying, 503, true)
	want := []Op{OpPathShut, OpReap, OpReleasePorts, OpDropFinalizer}
	if !hasOps(second.Ops, want...) {
		t.Fatalf("teardown ops = %s, want %v in that order", opsString(second.Ops), want)
	}
	if !second.RemoveFinalizer {
		t.Fatal("the finalizer was not released: the CR can never be deleted")
	}
	if h.host.reaps != 1 {
		t.Fatalf("reaps = %d, want 1", h.host.reaps)
	}
	if leases := h.ports.Leases(); len(leases) != 0 {
		t.Fatalf("teardown left %d leases behind: %v", len(leases), leases)
	}
	order := readOrder(t, h.logPath)
	if indexOf(order, "verify") < 0 {
		t.Fatal("teardown went through a bring-up path")
	}
}

func TestTeardownRefusesWhenTheGateCannotCompare(t *testing.T) {
	h := newHarness(t, false)
	up, _, _ := h.up()
	up.Status.NodeExpiry = nil
	up.Status.Conditions = append(up.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReachable,
		Status:             metav1.ConditionTrue,
		Reason:             "Probe",
		LastTransitionTime: metav1.NewTime(h.now),
	})
	dying := up
	dying.DeletionTimestamp = &metav1.Time{Time: h.now}

	h.host.reaps = 0
	res := h.reconcile(dying, 503, true)
	if !hasOps(res.Ops, OpBlocked) {
		t.Fatalf("ops = %s, want the block", opsString(res.Ops))
	}
	if cond := findCondition(res.Conditions, v1alpha1.ConditionTeardownBlocked); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("conditions[TeardownBlocked] = %+v, want True", cond)
	}
	if res.RemoveFinalizer {
		t.Fatal("the finalizer was released despite the refusal")
	}
	if h.host.reaps != 0 {
		t.Fatal("the group was reaped despite the refusal")
	}
	if _, ok := h.ports.PortFor(h.name, portallocator.PurposeKine); !ok {
		t.Fatal("ports were released despite the refusal")
	}
	if res.Alert == "" {
		t.Fatal("no alert on a blocked teardown")
	}
}

func TestInitTimeoutParksRatherThanLooping(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.local.Spawn(context.Background(), h.name, mustSteps(t, h)); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	c := h.cluster()
	c.Status.Phase = v1alpha1.ClusterInitializing
	c.Status.Instance = "instance-0"
	anchor := h.now.Add(-1 * time.Minute)
	c.Status.Conditions = []metav1.Condition{{
		Type:               ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             ReasonInitializing,
		LastTransitionTime: metav1.NewTime(anchor),
	}}

	res := h.reconcile(c, 0, false)
	if res.Phase != v1alpha1.ClusterInitializing {
		t.Fatalf("phase = %q, want Initializing", res.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("no requeue inside the deadline")
	}
	if got := mustCondition(t, res.Conditions, ConditionReady).LastTransitionTime.Time; !got.Equal(anchor) {
		t.Fatalf("the deadline anchor moved from %s to %s; a restart or a reconcile would restart the clock forever", anchor, got)
	}

	c.Status.Conditions[0].LastTransitionTime = metav1.NewTime(h.now.Add(-20 * time.Minute))
	res = h.reconcile(c, 0, false)
	if res.Phase != v1alpha1.ClusterUnavailable {
		t.Fatalf("phase = %q, want Unavailable", res.Phase)
	}
	if !hasOps(res.Ops, OpParked) {
		t.Fatalf("ops = %s", opsString(res.Ops))
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %s: the park must stop the loop", res.RequeueAfter)
	}
	if res.Alert == "" {
		t.Fatal("no alert on the park")
	}
	if cond := findCondition(res.Conditions, ConditionReady); cond == nil || cond.Reason != ReasonInitTimeout {
		t.Fatalf("conditions[Ready] = %+v, want reason %s", cond, ReasonInitTimeout)
	}
}

func TestWakeRefusesReadyWhileTheRevocationIsPending(t *testing.T) {
	h := newHarness(t, false)
	h.identityState = v1alpha1.IdentityRevoked
	h.saPresent = true

	c := h.cluster()
	first := h.reconcile(c, 0, false)
	c = apply(c, first)
	h.host.spawns = 0

	res := h.reconcile(c, 0, true)
	if res.Phase != v1alpha1.ClusterInitializing {
		t.Fatalf("phase = %q; a wake with an outstanding revocation effect must stay Initializing", res.Phase)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("no requeue: the wake would never complete")
	}
	if cond := mustCondition(t, res.Conditions, ConditionReady); cond.Reason != ReasonSettling {
		t.Fatalf("conditions[Ready].reason = %q, want %q", cond.Reason, ReasonSettling)
	}
	if h.host.spawns != 0 {
		t.Fatalf("the refused wake respawned the group (%d spawns)", h.host.spawns)
	}

	h.saPresent = false
	c = apply(c, res)
	done := h.reconcile(c, 0, true)
	if done.Phase != readyPhase(t) {
		t.Fatalf("phase = %q after the effect landed, want the settled wake phase", done.Phase)
	}
}

func mustSteps(t *testing.T, h *harness) []procgroup.Step {
	t.Helper()
	steps, err := stepBuilder(h.logPath)(Observed{}, Leases{})
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	return steps
}

type orderingHost struct {
	*Orphans

	mu     sync.Mutex
	events []string
}

func (h *orderingHost) note(event string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
}

func (h *orderingHost) Spawn(ctx context.Context, cluster string, steps []procgroup.Step) (*procgroup.Group, error) {
	h.note("spawn")
	return h.Orphans.Spawn(ctx, cluster, steps)
}

func (h *orderingHost) Reap(ctx context.Context, cluster string) error {
	h.note("reap")
	return h.Orphans.Reap(ctx, cluster)
}

func (h *orderingHost) Alive(cluster string) []string {
	h.note("alive")
	return h.Orphans.Alive(cluster)
}

func (h *orderingHost) Kill(ctx context.Context, handle string) error {
	h.note("kill:" + handle)
	return h.Orphans.Kill(ctx, handle)
}

func (h *orderingHost) order() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.events, " ")
}

type boundChild struct {
	cmd *exec.Cmd
	pid int
}

func spawnChild(t *testing.T, logPath, name string, argv []string, listen int) *boundChild {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Args = argv
	cmd.Env = append(os.Environ(),
		helperEnv+"=1", helperName+"="+name, helperLog+"="+logPath,
		helperListen+"="+strconv.Itoa(listen))
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return &boundChild{cmd: cmd, pid: cmd.Process.Pid}
}

func (c *boundChild) alive() bool { return syscall.Kill(c.pid, 0) == nil }

func waitBound(t *testing.T, port int, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never bound 127.0.0.1:%d: %v", name, port, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func freePort(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func restartAllocator(t *testing.T) *portallocator.Allocator {
	t.Helper()
	ports, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "restart-ports.json"),
		Range:     portallocator.Range{First: 20000, Last: 20099},
		Ephemeral: &portallocator.Range{First: 49152, Last: 65535},
		Probe:     freePort,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	return ports
}

func TestStartupReapsASurvivorItFoundByArgvAndLeavesAStranger(t *testing.T) {
	h := newHarness(t, false)
	ports := restartAllocator(t)
	mustLease := func(p portallocator.Purpose) portallocator.Lease {
		t.Helper()
		l, err := ports.Allocate(portallocator.Request{Cluster: h.name, Purpose: p})
		if err != nil {
			t.Fatalf("Allocate(%s): %v", p, err)
		}
		return l
	}
	survivorLease := mustLease(portallocator.PurposeKine)
	strangerLease := mustLease(portallocator.PurposePostgres)

	steps := mustSteps(t, h)
	argvs := make([][]string, 0, len(steps))
	for _, s := range steps {
		argvs = append(argvs, s.Spec.Argv)
	}

	survivor := spawnChild(t, h.logPath, "survivor", steps[0].Spec.Argv, survivorLease.Port)
	stranger := spawnChild(t, h.logPath, "stranger",
		[]string{"some-other-service", "-test.run=TestMain"}, strangerLease.Port)
	waitBound(t, survivorLease.Port, "survivor")
	waitBound(t, strangerLease.Port, "stranger")

	survivors, dropped, err := portscan.Scan(ports.Leases(), []portscan.Stack{{Cluster: h.name, Argvs: argvs}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(survivors[h.name]) != 1 || survivors[h.name][0] != strconv.Itoa(survivor.pid) {
		t.Fatalf("survivors = %v, want only the child whose argv is the step's own argv (%d); "+
			"the survivor path must find a process by what it was spawned as, not by holding a "+
			"leased port", survivors, survivor.pid)
	}
	if len(dropped) != 1 || dropped[0].PID != stranger.pid {
		t.Fatalf("drops = %+v, want exactly one drop for the child whose argv is not ours (%d)", dropped, stranger.pid)
	}
	for _, d := range dropped {
		if d.PID == survivor.pid {
			t.Fatalf("the survivor was also dropped: %+v", d)
		}
	}

	orphans := NewOrphans(h.local, survivors)
	host := &orderingHost{Orphans: orphans}
	rec, err := New(Options{
		Host:        host,
		Ports:       ports,
		Networks:    networks(t),
		Steps:       stepBuilder(h.logPath),
		Identity:    guard(h.src, h.dataDir, h.logPath),
		NewInstance: func() string { return "gen-restart" },
		Now:         func() time.Time { return h.now },
		InitTimeout: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	results, err := rec.Startup(context.Background(), []Observed{h.observe(h.cluster(), 0, false)})
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Startup returned %d results, want 1", len(results))
	}
	res := results[0]
	if !hasOps(res.Ops, OpReapOrphans, OpInstanceBumped, OpSpawn) {
		t.Fatalf("ops = %s, want the survivor reap, the bump and a spawn", opsString(res.Ops))
	}

	order := host.order()
	want := "reap alive kill:" + strconv.Itoa(survivor.pid) + " spawn"
	if order != want {
		t.Fatalf("the restart reached the host as %q, want %q -- a second process group may not "+
			"be spawned before the group that still holds the store is reaped", order, want)
	}

	_ = survivor.cmd.Wait()
	if survivor.alive() {
		t.Fatalf("the survivor %d is still there after the restart: an apiserver left running "+
			"under a fresh group is two writers on one store", survivor.pid)
	}
	if !stranger.alive() {
		t.Fatalf("the negative control %d was killed: its argv never matched this cluster, so "+
			"its pid must never have become a handle -- without this the test proves nothing, "+
			"because a reap that kills everything passes the assertion above too", stranger.pid)
	}
	if still := orphans.Alive(h.name); len(still) != 0 {
		t.Fatalf("the reaped handle is still recorded: %v", still)
	}

	if err := host.Reap(context.Background(), h.name); err != nil {
		t.Fatalf("Reap after the restart: %v", err)
	}
	if _, err := host.Spawn(context.Background(), h.name, steps); err != nil {
		t.Fatalf("Spawn after a reap: %v -- the restart must leave the host able to hold the "+
			"group again", err)
	}
	if err := host.Reap(context.Background(), h.name); err != nil {
		t.Fatalf("final Reap: %v", err)
	}
}

func findCondition(conds []metav1.Condition, want string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == want {
			return &conds[i]
		}
	}
	return nil
}

func mustCondition(t *testing.T, conds []metav1.Condition, want string) *metav1.Condition {
	t.Helper()
	c := findCondition(conds, want)
	if c == nil {
		t.Fatalf("no %s condition in %+v", want, conds)
	}
	return c
}

func indexOfStrings(ops []Op, want Op) int {
	for i, o := range ops {
		if o == want {
			return i
		}
	}
	return -1
}
