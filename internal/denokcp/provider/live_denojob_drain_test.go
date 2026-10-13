//go:build linux

package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/provider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/execrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/policyclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func liveEnvInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func liveEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

type countingRuntime struct {
	provider.Runtime

	mu sync.Mutex

	calls int

	nanos int64

	byName map[string]int
}

func (c *countingRuntime) enter() func() {
	name := "?"
	if pc, _, _, ok := runtime.Caller(1); ok {
		if fn := runtime.FuncForPC(pc); fn != nil {
			full := fn.Name()
			if i := strings.LastIndex(full, "."); i >= 0 {
				name = full[i+1:]
			}
		}
	}
	start := time.Now()
	c.mu.Lock()
	c.calls++
	if c.byName == nil {
		c.byName = map[string]int{}
	}
	c.byName[name]++
	c.mu.Unlock()
	return func() {
		d := int64(time.Since(start))
		c.mu.Lock()
		c.nanos += d
		c.mu.Unlock()
	}
}

func (c *countingRuntime) snapshot() (int, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.nanos
}

func (c *countingRuntime) breakdown() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.byName))
	for name := range c.byName {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return c.byName[names[i]] > c.byName[names[j]] })
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, c.byName[name]))
	}
	return strings.Join(parts, " ")
}

func (c *countingRuntime) Read(ctx context.Context, ref provider.Ref) (*v1alpha1.PolicyWorkflowRun, error) {
	defer c.enter()()
	return c.Runtime.Read(ctx, ref)
}

func (c *countingRuntime) WriteStatus(ctx context.Context, ref provider.Ref, st v1alpha1.PolicyWorkflowRunStatus) error {
	defer c.enter()()
	return c.Runtime.WriteStatus(ctx, ref, st)
}

func (c *countingRuntime) Delete(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.Delete(ctx, ref)
}

func (c *countingRuntime) RemoveFinalizer(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.RemoveFinalizer(ctx, ref)
}

func (c *countingRuntime) CreateRun(ctx context.Context, logicalCluster string, run *v1alpha1.DenoRun) error {
	defer c.enter()()
	return c.Runtime.CreateRun(ctx, logicalCluster, run)
}

func (c *countingRuntime) WriteRunStatus(ctx context.Context, ref provider.Ref, st v1alpha1.DenoRunStatus) error {
	defer c.enter()()
	return c.Runtime.WriteRunStatus(ctx, ref, st)
}

func (c *countingRuntime) DeleteRun(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.DeleteRun(ctx, ref)
}

func (c *countingRuntime) RemoveRunFinalizer(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.RemoveRunFinalizer(ctx, ref)
}

func (c *countingRuntime) CreatePod(ctx context.Context, logicalCluster string, pod *v1alpha1.DenoPod) error {
	defer c.enter()()
	return c.Runtime.CreatePod(ctx, logicalCluster, pod)
}

func (c *countingRuntime) WritePodStatus(ctx context.Context, ref provider.Ref, st v1alpha1.DenoPodStatus) error {
	defer c.enter()()
	return c.Runtime.WritePodStatus(ctx, ref, st)
}

func (c *countingRuntime) DeletePod(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.DeletePod(ctx, ref)
}

func (c *countingRuntime) RemovePodFinalizer(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.RemovePodFinalizer(ctx, ref)
}

func (c *countingRuntime) CreateJob(ctx context.Context, logicalCluster string, job *v1alpha1.DenoJob) error {
	defer c.enter()()
	return c.Runtime.CreateJob(ctx, logicalCluster, job)
}

func (c *countingRuntime) WriteJobStatus(ctx context.Context, ref provider.Ref, st v1alpha1.DenoJobStatus) error {
	defer c.enter()()
	return c.Runtime.WriteJobStatus(ctx, ref, st)
}

func (c *countingRuntime) DeleteJob(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.DeleteJob(ctx, ref)
}

func (c *countingRuntime) WriteTriggerStatus(ctx context.Context, ref provider.Ref, st v1alpha1.RunTriggerStatus) error {
	defer c.enter()()
	return c.Runtime.WriteTriggerStatus(ctx, ref, st)
}

func (c *countingRuntime) WriteEngineStatus(ctx context.Context, ref provider.Ref, st v1alpha1.PolicyEngineStatus) error {
	defer c.enter()()
	return c.Runtime.WriteEngineStatus(ctx, ref, st)
}

func (c *countingRuntime) DeleteEngine(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.DeleteEngine(ctx, ref)
}

func (c *countingRuntime) RemoveEngineFinalizer(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.RemoveEngineFinalizer(ctx, ref)
}

func (c *countingRuntime) WriteWorkflowPodStatus(ctx context.Context, ref provider.Ref, st v1alpha1.PolicyWorkflowPodStatus) error {
	defer c.enter()()
	return c.Runtime.WriteWorkflowPodStatus(ctx, ref, st)
}

func (c *countingRuntime) DeleteWorkflowPod(ctx context.Context, ref provider.Ref) error {
	defer c.enter()()
	return c.Runtime.DeleteWorkflowPod(ctx, ref)
}

func (c *countingRuntime) CreateWorkflowRun(ctx context.Context, logicalCluster string, run *v1alpha1.PolicyWorkflowRun) error {
	defer c.enter()()
	return c.Runtime.CreateWorkflowRun(ctx, logicalCluster, run)
}

func TestDenoJobAllowDrainOnRealKCP(t *testing.T) {
	if !livegate.RequiresLive() {
		t.Skip("set DENO_KCP_REQUIRE_LIVE=1 and provide kcp, kine, kubectl and deno")
	}
	requireBinary(t, envOr("KCP_BIN", "kcp"))
	requireBinary(t, envOr("KINE_BIN", "kine"))
	requireBinary(t, "kubectl")
	requireBinary(t, envOr("DENO_BIN", "deno"))

	runs := liveEnvInt("DENOJOB_RUNS", 100)
	parallelism := liveEnvInt("DENOJOB_PARALLELISM", 20)
	interval := liveEnvDuration("DENOJOB_INTERVAL", 2*time.Second)
	minPoll := liveEnvDuration("DENOJOB_MIN_POLL", 0)
	watchWorkers := liveEnvInt("DENOJOB_WATCH_WORKERS", 0)

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	deployDir := filepath.Join(repoRoot, "deploy", "denokcp")

	root := t.TempDir()
	kinePort := freePort(t)
	kcpPort := freePort(t)
	kineEndpoint := fmt.Sprintf("http://127.0.0.1:%d", kinePort)
	kubeconfig := filepath.Join(root, "admin.kubeconfig")

	startProcess(t, filepath.Join(root, "kine.log"), envOr("KINE_BIN", "kine"),
		"--endpoint", "sqlite://"+filepath.Join(root, "kine.db"),
		"--listen-address", fmt.Sprintf("127.0.0.1:%d", kinePort),
		"--metrics-bind-address=0")
	waitFor(t, "kine", 30*time.Second, func() bool { return tcpOpen(fmt.Sprintf("127.0.0.1:%d", kinePort)) })

	startProcess(t, filepath.Join(root, "kcp.log"), envOr("KCP_BIN", "kcp"),
		"start",
		"--root-directory="+root,
		"--etcd-servers="+kineEndpoint,
		"--bind-address=127.0.0.1",
		"--secure-port="+fmt.Sprint(kcpPort),
		"--feature-gates=WorkspaceMounts=true")
	waitFor(t, "kcp", 90*time.Second, func() bool {
		if _, err := os.Stat(kubeconfig); err != nil {
			return false
		}
		return httpOK(fmt.Sprintf("https://127.0.0.1:%d/readyz", kcpPort))
	})

	host := fmt.Sprintf("https://127.0.0.1:%d", kcpPort)
	rootCluster := host + "/clusters/root"
	providerCluster := host + "/clusters/root:deno-provider"
	tenantCluster := host + "/clusters/root:runtime"

	kubectlApply(t, kubeconfig, rootCluster, `
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: deno-provider
spec:
  type:
    name: universal
    path: root
`)
	waitFor(t, "provider workspace Ready", 60*time.Second, func() bool {
		return kubectl(t, kubeconfig, rootCluster, "get", "workspace", "deno-provider",
			"-o", "jsonpath={.status.phase}") == "Ready"
	})

	installProviderAPIs(t, kubeconfig, providerCluster, rootCluster, deployDir)

	for _, export := range []string{"policyworkflowruns", "denoruntime"} {
		export := export
		waitFor(t, "apiexport "+export+" IdentityValid", 60*time.Second, func() bool {
			return kubectl(t, kubeconfig, providerCluster, "get", "apiexport", export,
				"-o", `jsonpath={.status.conditions[?(@.type=="IdentityValid")].status}`) == "True"
		})
	}

	kubectlApply(t, kubeconfig, rootCluster, `
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: runtime
spec:
  type:
    name: denoruntime
    path: root
`)
	waitFor(t, "tenant API bound", 60*time.Second, func() bool {
		_, err := kubectlTry(t, kubeconfig, tenantCluster, "get", "denojobs")
		return err == nil
	})

	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRegistry(provider.RegistryOptions{Host: restCfg.Host, RestConfig: restCfg})
	if err != nil {
		t.Fatal(err)
	}
	podRunner, err := execrunner.NewPod(execrunner.PodOptions{
		DenoBin: envOr("DENO_BIN", "deno"),
		RunsDir: filepath.Join(root, "pods"),
		Timeout: 2 * time.Minute,
		CAData:  caData(t, restCfg.CAData, restCfg.CAFile),
	})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingRuntime{Runtime: registry}
	p, err := provider.New(provider.Options{
		Registry:          counted,
		PolicyClient:      policyclient.New(),
		Runtime:           counted,
		RestConfig:        restCfg,
		ProviderWorkspace: "root:deno-provider",
		Minter:            registry,
		PodRunner:         podRunner,
		Host:              restCfg.Host,
		TokenTTL:          time.Hour,
		WriteStatus:       true,
		Interval:          interval,
		MinTransitionPoll: minPoll,
		WatchWorkers:      watchWorkers,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loopDone := make(chan struct{})
	var runErr error
	go func() {
		defer close(loopDone)
		runErr = p.Run(ctx)
	}()

	completions := int32(runs)
	par := int32(parallelism)
	job := &v1alpha1.DenoJob{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "DenoJob"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-drain",
			Namespace: "default",
		},
		Spec: v1alpha1.DenoJobSpec{
			Completions: &completions,
			Parallelism: &par,
			Template: v1alpha1.DenoRunSpec{
				DenoPodTemplate: v1alpha1.DenoPodTemplate{
					Script:      "await Deno.writeTextFile(\"result.json\", JSON.stringify({ allow: \"true\" }));\n",
					Permissions: &v1alpha1.DenoPermissions{Write: &v1alpha1.DenoPermission{AllowList: []string{"result.json"}}},
				},
			},
		},
	}

	jobRef := provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: job.Name}
	baseCalls, baseNanos := counted.snapshot()
	baseReconciles := p.Reconciles()
	admissionStarted := time.Now()
	var firstSeen time.Time
	if err := registry.CreateJob(ctx, "root:runtime", job); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Minute)
	var lastJob *v1alpha1.DenoJob
	var peakActive int
	var admissionElapsed time.Duration
	var admissionDone bool
	var totalElapsed time.Duration
	for {
		list, err := registry.ListRuns(ctx, "root:runtime")
		if err != nil {
			t.Fatal(err)
		}
		children := jobChildren(list, jobRef.Name)
		active := 0
		for i := range children {
			switch children[i].Status.Phase {
			case v1alpha1.DenoRunSucceeded, v1alpha1.DenoRunFailed:
			default:
				active++
			}
		}
		if active > peakActive {
			peakActive = active
		}
		if !admissionDone && len(children) >= runs {
			admissionDone = true
			admissionElapsed = time.Since(admissionStarted)
		}
		got, err := registry.ReadJob(ctx, jobRef)
		if err == nil {
			lastJob = got
			if firstSeen.IsZero() {
				firstSeen = time.Now()
			}
			if got.Status.Phase == v1alpha1.DenoJobSucceeded || got.Status.Phase == v1alpha1.DenoJobFailed {
				totalElapsed = time.Since(admissionStarted)
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the denojob did not reach a terminal phase in 20m; last phase %q children=%d succeeded=%d",
				jobPhaseOf(lastJob), len(children), jobSucceededOf(lastJob))
		}
		time.Sleep(500 * time.Millisecond)
	}

	cancel()
	<-loopDone
	if runErr != nil {
		t.Fatalf("provider run: %v", runErr)
	}
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer verifyCancel()

	endCalls, endNanos := counted.snapshot()
	endReconciles := p.Reconciles()
	calls := endCalls - baseCalls
	restNanos := endNanos - baseNanos
	reconciles := endReconciles - baseReconciles
	t.Logf("  provider calls by method: %s", counted.breakdown())

	if lastJob == nil {
		t.Fatal("the denojob was never observed")
	}
	if lastJob.Status.Phase != v1alpha1.DenoJobSucceeded {
		t.Fatalf("job phase = %s, want Succeeded (succeeded=%d failed=%d retries=%d)",
			lastJob.Status.Phase, lastJob.Status.Succeeded, lastJob.Status.Failed, lastJob.Status.Retries)
	}
	if lastJob.Status.Succeeded != int32(runs) {
		t.Fatalf("job status succeeded = %d, want %d", lastJob.Status.Succeeded, runs)
	}

	list, err := registry.ListRuns(verifyCtx, "root:runtime")
	if err != nil {
		t.Fatal(err)
	}
	children := jobChildren(list, jobRef.Name)
	if len(children) != runs {
		t.Fatalf("job created %d child denoruns, want %d", len(children), runs)
	}
	var succeeded, failed, pendingOrRunning, allowed int
	var runElapsed []time.Duration
	var wallElapsed []time.Duration
	for i := range children {
		run := children[i]
		switch run.Status.Phase {
		case v1alpha1.DenoRunSucceeded:
			succeeded++
			if run.Status.Outputs["allow"] == "true" {
				allowed++
			}
		case v1alpha1.DenoRunFailed:
			failed++
		default:
			pendingOrRunning++
		}
		if run.Status.StartTime != nil && run.Status.CompletionTime != nil {
			runElapsed = append(runElapsed, run.Status.CompletionTime.Sub(run.Status.StartTime.Time))
		}
		if run.Status.CompletionTime != nil && !run.CreationTimestamp.IsZero() {
			wallElapsed = append(wallElapsed, run.Status.CompletionTime.Sub(run.CreationTimestamp.Time))
		}
	}
	sort.Slice(runElapsed, func(i, j int) bool { return runElapsed[i] < runElapsed[j] })
	sort.Slice(wallElapsed, func(i, j int) bool { return wallElapsed[i] < wallElapsed[j] })

	if succeeded != runs || allowed != runs {
		t.Fatalf("child denoruns succeeded=%d allow=true=%d, want %d of each (failed=%d pendingOrRunning=%d)",
			succeeded, allowed, runs, failed, pendingOrRunning)
	}
	if peakActive > parallelism {
		t.Fatalf("peak observed active denoruns %d exceeded parallelism %d", peakActive, parallelism)
	}

	t.Logf("denojob allow drain: interval=%s minPollFlag=%s watchWorkersFlag=%d completions=%d parallelism=%d children=%d",
		interval, minPoll, watchWorkers, runs, parallelism, len(children))
	if admissionDone {
		t.Logf("  admission elapsed=%s (%.0f creates/s)", admissionElapsed.Round(time.Millisecond),
			float64(runs)/admissionElapsed.Seconds())
	} else {
		t.Logf("  admission never observed all %d children", runs)
	}
	t.Logf("  total elapsed to all-terminal=%s", totalElapsed.Round(time.Millisecond))
	t.Logf("  completion rate=%d/%d succeeded=%d failed=%d cancelled=%d", succeeded, runs, succeeded, failed, 0)
	t.Logf("  peak observed active=%d", peakActive)
	reportDurations(t, "run StartTime->Completion", runElapsed)
	reportDurations(t, "run CreationTimestamp->Completion", wallElapsed)
	if !firstSeen.IsZero() {
		t.Logf("  create->first-seen=%s first-seen->terminal=%s",
			firstSeen.Sub(admissionStarted).Round(time.Millisecond), time.Since(firstSeen).Round(time.Millisecond))
	}
	avgCallNanos := int64(0)
	if calls > 0 {
		avgCallNanos = restNanos / int64(calls)
	}
	t.Logf("  provider reconciles=%d", reconciles)
	t.Logf("  provider max active runs=%d still active=%d starts=%d", p.MaxActiveRuns(), p.ActiveRuns(), p.RunStarts())
	if left := p.ActiveRuns(); left != 0 {
		t.Fatalf("active runs = %d after every child reached a terminal phase, want 0", left)
	}
	t.Logf("  provider runtime calls=%d restWall=%s avg/call=%s",
		calls, time.Duration(restNanos).Round(time.Millisecond),
		time.Duration(avgCallNanos).Round(time.Microsecond))
	t.Logf("  attribution: total=%s restWall=%s other=%s deno-exec~0.2s",
		totalElapsed.Round(time.Millisecond), time.Duration(restNanos).Round(time.Millisecond),
		(totalElapsed - time.Duration(restNanos)).Round(time.Millisecond))
}

func reportDurations(t *testing.T, label string, d []time.Duration) {
	t.Helper()
	if len(d) == 0 {
		return
	}
	t.Logf("  %s min=%s median=%s max=%s n=%d", label,
		d[0].Round(time.Millisecond), d[len(d)/2].Round(time.Millisecond),
		d[len(d)-1].Round(time.Millisecond), len(d))
}

func jobChildren(runs []v1alpha1.DenoRun, jobName string) []v1alpha1.DenoRun {
	var out []v1alpha1.DenoRun
	for i := range runs {
		if runs[i].Labels[provider.JobRunLabel] == jobName {
			out = append(out, runs[i])
		}
	}
	return out
}

func jobSucceededOf(job *v1alpha1.DenoJob) int32 {
	if job == nil {
		return 0
	}
	return job.Status.Succeeded
}

func jobPhaseOf(job *v1alpha1.DenoJob) string {
	if job == nil {
		return "<none>"
	}
	return string(job.Status.Phase)
}
