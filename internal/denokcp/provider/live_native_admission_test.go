//go:build linux

package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func TestNativeAdmissionAndQueueDrainOnRealKCP(t *testing.T) {
	if !livegate.RequiresLive() {
		t.Skip("set DENO_KCP_REQUIRE_LIVE=1 and provide kcp, kine, kubectl, deno and the policy engine")
	}
	requireBinary(t, envOr("KCP_BIN", "kcp"))
	requireBinary(t, envOr("KINE_BIN", "kine"))
	requireBinary(t, "kubectl")
	requireBinary(t, envOr("DENO_BIN", "deno"))

	runs := liveEnvInt("DRAIN_RUNS", 24)
	concurrency := liveEnvInt("DRAIN_CONCURRENCY", 4)
	creators := liveEnvInt("DRAIN_CREATORS", 64)

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	deployDir := filepath.Join(repoRoot, "deploy", "denokcp")
	serverDir := envOr("POLICY_ENGINE_DIR", filepath.Join(repoRoot, "..", "policy-engine", "lib", "policy-engine-server-gha-lite"))
	actionsDir := envOr("BUNDLED_ACTIONS_DIR", filepath.Join(repoRoot, "..", "policy-engine", "lib", "policies", "gha-lite", "bundled-actions"))
	if _, err := os.Stat(filepath.Join(serverDir, "main.ts")); err != nil {
		livegate.Require(t, "the policy engine was not found at %s", serverDir)
	}

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
		_, err := kubectlTry(t, kubeconfig, tenantCluster, "get", "policyworkflowruns")
		return err == nil
	})

	kubectlApply(t, kubeconfig, tenantCluster, `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: deno-runner
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: deno-runner-fire
rules:
  - apiGroups: ["deno.computer"]
    resources: ["policyworkflowruns"]
    verbs: ["get", "list", "create"]
  - apiGroups: ["deno.computer"]
    resources: ["policyworkflowpods"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: deno-runner-fire
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: deno-runner-fire
subjects:
  - kind: ServiceAccount
    name: deno-runner
    namespace: default
`)

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
	engineRunner, err := execrunner.NewEngine(execrunner.EngineOptions{
		DenoBin:   envOr("DENO_BIN", "deno"),
		ServerDir: serverDir,
		RunsDir:   filepath.Join(root, "engines"),
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := provider.New(provider.Options{
		Registry:          registry,
		PolicyClient:      policyclient.New(),
		Runtime:           registry,
		RestConfig:        restCfg,
		ProviderWorkspace: "root:deno-provider",
		Minter:            registry,
		PodRunner:         podRunner,
		EngineRunner:      engineRunner,
		Host:              restCfg.Host,
		TokenTTL:          time.Hour,
		BundledActionsDir: actionsDir,
		WriteStatus:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := p.Run(ctx); err != nil {
			t.Logf("provider loop stopped: %v", err)
		}
	}()

	kubectlApply(t, kubeconfig, tenantCluster, readFile(t, filepath.Join(deployDir, "examples", "policyengine.yaml")))
	waitFor(t, "policy engine Ready", 120*time.Second, func() bool {
		e, err := registry.ReadEngine(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "policy-engine"})
		return err == nil && e.Status.Ready
	})

	kubectlApply(t, kubeconfig, tenantCluster, drainPodManifest("open-policy-pod", concurrency))
	waitFor(t, "drain pod Running", 60*time.Second, func() bool {
		w, err := registry.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "open-policy-pod"})
		return err == nil && w.Status.Phase == v1alpha1.PolicyWorkflowPodRunning && w.Status.Endpoint != ""
	})

	t.Run("client creates the run through the KCP API under RBAC", func(t *testing.T) {
		kubectlApply(t, kubeconfig, tenantCluster, readFile(t, filepath.Join(deployDir, "examples", "native-fire-pod.yaml")))
		waitFor(t, "native fire pod Succeeded", 120*time.Second, func() bool {
			pod, err := registry.ReadPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "native-fire-open-policy"})
			return err == nil && pod.Status.Phase == v1alpha1.DenoPodSucceeded
		})
		firePod, err := registry.ReadPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "native-fire-open-policy"})
		if err != nil {
			t.Fatal(err)
		}
		if got := firePod.Status.Outputs["httpStatus"]; got != "201" {
			t.Fatalf("native fire pod outputs = %v, want httpStatus=201", firePod.Status.Outputs)
		}
		var created *v1alpha1.PolicyWorkflowRun
		waitForWith(t, "native run Succeeded", liveWait("DRAIN_WAIT", 45*time.Second), 5*time.Second, func() bool {
			list, err := registry.ListWorkflowRuns(ctx, "root:runtime")
			if err != nil {
				return false
			}
			created = newestRunForPod(list, "open-policy-pod")
			return created != nil && created.Status.Phase == v1alpha1.PolicyWorkflowSucceeded
		}, func() { dumpWorkflowState(t, ctx, registry, "root:runtime") })
		if created.Spec.PolicyWorkflowPod != "open-policy-pod" {
			t.Fatalf("native run pod ref = %q", created.Spec.PolicyWorkflowPod)
		}
		if created.Status.Outputs["allow"] != "true" {
			t.Fatalf("native run outputs = %v, want allow=true", created.Status.Outputs)
		}
		t.Logf("native client created %s labelled %s and it resolved allow=%s",
			created.Name, created.Labels[v1alpha1.PolicyWorkflowPodLabel], created.Status.Outputs["allow"])
	})

	pod, err := registry.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "open-policy-pod"})
	if err != nil {
		t.Fatal(err)
	}

	admissionStarted := time.Now()
	created := createDrainRuns(t, registry, ctx, pod, runs, creators)
	admissionElapsed := time.Since(admissionStarted)

	deadline := time.Now().Add(liveWait("DRAIN_WAIT", 120*time.Second))
	var terminal, peak, queueHigh int
	for {
		list, err := registry.ListWorkflowRuns(ctx, "root:runtime")
		if err != nil {
			t.Fatal(err)
		}
		var pending int
		terminal, peak, pending = countDrainRuns(list, runs, peak)
		if pending > queueHigh {
			queueHigh = pending
		}
		if terminal >= created {
			break
		}
		if time.Now().After(deadline) {
			dumpWorkflowState(t, ctx, registry, "root:runtime")
			t.Fatalf("only %d of %d drain runs reached a terminal phase before the wait budget", terminal, created)
		}
		time.Sleep(time.Second)
	}
	totalElapsed := time.Since(admissionStarted)

	list, err := registry.ListWorkflowRuns(ctx, "root:runtime")
	if err != nil {
		t.Fatal(err)
	}
	succeeded, failed, cancelled, _ := phaseCounts(list, runs)
	t.Logf("drain: runs=%d created=%d concurrency=%d", runs, created, concurrency)
	t.Logf("  admission elapsed=%s (%.0f creates/s)", admissionElapsed.Round(time.Millisecond),
		float64(created)/admissionElapsed.Seconds())
	t.Logf("  total elapsed to all-terminal=%s", totalElapsed.Round(time.Millisecond))
	t.Logf("  completion rate=%d/%d succeeded=%d failed=%d cancelled=%d rejections=0", succeeded, created, succeeded, failed, cancelled)
	t.Logf("  peak executing=%d queue high-water=%d", peak, queueHigh)

	if created != runs {
		t.Fatalf("created %d of %d runs, want all accepted (rejections=%d)", created, runs, runs-created)
	}
	if peak > concurrency {
		t.Fatalf("peak executing %d exceeded maxConcurrent %d", peak, concurrency)
	}
	if succeeded != created {
		t.Fatalf("succeeded=%d, want all %d drain runs to succeed", succeeded, created)
	}
}

func createDrainRuns(t *testing.T, registry provider.Runtime, ctx context.Context, pod *v1alpha1.PolicyWorkflowPod, runs, creators int) int {
	t.Helper()
	var wg sync.WaitGroup
	sem := make(chan struct{}, creators)
	errs := make(chan error, runs)
	for i := 0; i < runs; i++ {
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := createRunWithRetry(ctx, registry, nativeRun(pod, fmt.Sprintf("drain-%d", i))); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("creating a drain run: %v", err)
	}
	return runs
}

func nativeRun(pod *v1alpha1.PolicyWorkflowPod, name string) *v1alpha1.PolicyWorkflowRun {
	controller := true
	block := true
	return &v1alpha1.PolicyWorkflowRun{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "PolicyWorkflowRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "default",
			Finalizers:      []string{v1alpha1.FinalizerPolicyWorkflowRun},
			Labels:          map[string]string{v1alpha1.PolicyWorkflowPodLabel: pod.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "PolicyWorkflowPod", Name: pod.Name, UID: pod.UID, Controller: &controller, BlockOwnerDeletion: &block}},
		},
		Spec: v1alpha1.PolicyWorkflowRunSpec{PolicyWorkflowPod: pod.Name},
	}
}

func createRunWithRetry(ctx context.Context, registry provider.Runtime, run *v1alpha1.PolicyWorkflowRun) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = registry.CreateWorkflowRun(ctx, "root:runtime", run)
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "database is locked") {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	return err
}

func countDrainRuns(list []v1alpha1.PolicyWorkflowRun, total int, peak int) (terminal, peakOut, pendingOut int) {
	peakOut = peak
	running := 0
	pending := 0
	for i := range list {
		if !drainRunName(list[i].Name, total) {
			continue
		}
		switch list[i].Status.Phase {
		case v1alpha1.PolicyWorkflowSucceeded, v1alpha1.PolicyWorkflowFailed, v1alpha1.PolicyWorkflowCancelled:
			terminal++
		case v1alpha1.PolicyWorkflowRunning:
			running++
		default:
			pending++
		}
	}
	if running > peakOut {
		peakOut = running
	}
	return terminal, peakOut, pending
}

func phaseCounts(list []v1alpha1.PolicyWorkflowRun, total int) (succeeded, failed, cancelled, pendingOrRunning int) {
	for i := range list {
		if !drainRunName(list[i].Name, total) {
			continue
		}
		switch list[i].Status.Phase {
		case v1alpha1.PolicyWorkflowSucceeded:
			succeeded++
		case v1alpha1.PolicyWorkflowFailed:
			failed++
		case v1alpha1.PolicyWorkflowCancelled:
			cancelled++
		default:
			pendingOrRunning++
		}
	}
	return succeeded, failed, cancelled, pendingOrRunning
}

func drainRunName(name string, total int) bool {
	if len(name) <= len("drain-") || name[:len("drain-")] != "drain-" {
		return false
	}
	n, err := strconv.Atoi(name[len("drain-"):])
	return err == nil && n >= 0 && n < total
}

func dumpWorkflowState(t *testing.T, ctx context.Context, rt provider.Reader, lc string) {
	t.Helper()
	runs, err := rt.ListWorkflowRuns(ctx, lc)
	if err != nil {
		t.Logf("dump: list runs: %v", err)
		return
	}
	var pending, running, succeeded, failed, cancelled int
	for i := range runs {
		switch runs[i].Status.Phase {
		case v1alpha1.PolicyWorkflowRunning:
			running++
		case v1alpha1.PolicyWorkflowSucceeded:
			succeeded++
		case v1alpha1.PolicyWorkflowFailed:
			failed++
		case v1alpha1.PolicyWorkflowCancelled:
			cancelled++
		default:
			pending++
		}
	}
	t.Logf("dump: %d runs pending=%d running=%d succeeded=%d failed=%d cancelled=%d",
		len(runs), pending, running, succeeded, failed, cancelled)
	for i := range runs {
		if i >= 5 {
			break
		}
		r := &runs[i]
		reason, message := "", ""
		if len(r.Status.Conditions) > 0 {
			c := r.Status.Conditions[len(r.Status.Conditions)-1]
			reason, message = c.Reason, c.Message
		}
		t.Logf("  run %s pod=%s phase=%s runID=%q active=%d reason=%s message=%q",
			r.Name, r.Labels[v1alpha1.PolicyWorkflowPodLabel], r.Status.Phase, r.Status.RunID, r.Status.Active, reason, message)
	}
	pod, err := rt.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: lc, Namespace: "default", Name: "open-policy-pod"})
	if err != nil {
		t.Logf("dump: read pod: %v", err)
		return
	}
	if pod == nil {
		t.Logf("dump: pod open-policy-pod not found")
		return
	}
	t.Logf("dump: pod open-policy-pod phase=%s endpoint=%q active=%d",
		pod.Status.Phase, pod.Status.Endpoint, pod.Status.Active)
}

func drainPodManifest(name string, concurrency int) string {
	return fmt.Sprintf(`apiVersion: deno.computer/v1alpha1
kind: PolicyWorkflowPod
metadata:
  name: %s
  namespace: default
spec:
  policyEngine: policy-engine
  concurrencyPolicy: Allow
  maxConcurrent: %d
  inputs:
    self-did: did:plc:example
  workflow:
    name: open policy
    jobs:
      evaluate:
        runs-on: self-hosted
        steps:
        - uses: tangy/policy-open@v1
          id: policy
          with:
            self-did: ${{ inputs.self-did || '' }}
        - run: test "${{ steps.policy.outputs.allow }}" = "true"
`, name, concurrency)
}
