//go:build linux

package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/provider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/execrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/policyclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestTheFullDenoRuntimeLifecycleOnRealKCP(t *testing.T) {
	if !livegate.RequiresLive() {
		t.Skip("set DENO_KCP_REQUIRE_LIVE=1 and provide kcp, kine, kubectl, deno and the policy engine")
	}
	requireBinary(t, envOr("KCP_BIN", "kcp"))
	requireBinary(t, envOr("KINE_BIN", "kine"))
	requireBinary(t, "kubectl")
	requireBinary(t, envOr("DENO_BIN", "deno"))

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

	// ponytail: the schemas are globbed rather than listed, because a list here
	// is a list that a new kind forgets: the export names the schema, so an export
	// applied over a missing one binds nothing and every test in this package
	// fails with "timed out waiting for tenant API bound" instead of naming the
	// kind nobody installed.
	schemas, err := filepath.Glob(filepath.Join(deployDir, "*-apiresourceschema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) == 0 {
		t.Fatalf("no APIResourceSchemas under %s", deployDir)
	}
	sort.Strings(schemas)
	for _, path := range schemas {
		kubectlApply(t, kubeconfig, providerCluster, readFile(t, path))
	}
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
		_, err := kubectlTry(t, kubeconfig, tenantCluster, "get", "policyengines")
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
  name: deno-runner-read
rules:
  - apiGroups: ["deno.computer"]
    resources: ["policyworkflowruns"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: deno-runner-read
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: deno-runner-read
subjects:
  - kind: ServiceAccount
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

	kubectlApply(t, kubeconfig, tenantCluster, readFile(t, filepath.Join(deployDir, "examples", "policyworkflowpod.yaml")))
	waitFor(t, "policy workflow pod Running", 60*time.Second, func() bool {
		w, err := registry.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "open-policy-pod"})
		return err == nil && w.Status.Phase == v1alpha1.PolicyWorkflowPodRunning && w.Status.Endpoint != ""
	})

	kubectlApply(t, kubeconfig, tenantCluster, readFile(t, filepath.Join(deployDir, "examples", "runtrigger.yaml")))
	triggerRef := provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "on-policy-allow"}
	waitFor(t, "trigger Pending before any run finished", 60*time.Second, func() bool {
		tr, err := registry.ReadTrigger(ctx, triggerRef)
		return err == nil && tr.Status.Phase == v1alpha1.RunTriggerPending
	})

	kubectlApply(t, kubeconfig, tenantCluster, readFile(t, filepath.Join(deployDir, "examples", "native-fire-pod.yaml")))
	waitFor(t, "native run creator Succeeded", 120*time.Second, func() bool {
		pod, err := registry.ReadPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "native-fire-open-policy"})
		return err == nil && pod.Status.Phase == v1alpha1.DenoPodSucceeded
	})
	firePod, err := registry.ReadPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "native-fire-open-policy"})
	if err != nil {
		t.Fatal(err)
	}
	if firePod.Status.Outputs["httpStatus"] != "201" {
		t.Fatalf("native run creator outputs = %v, want httpStatus=201", firePod.Status.Outputs)
	}

	deadline := time.Now().Add(3 * time.Minute)
	var run *v1alpha1.PolicyWorkflowRun
	var terminalAt time.Time
	for {
		runs, err := registry.ListWorkflowRuns(ctx, "root:runtime")
		if err != nil {
			t.Fatal(err)
		}
		run = newestRunForPod(runs, "open-policy-pod")
		if run != nil && run.Status.Phase == v1alpha1.PolicyWorkflowSucceeded {
			terminalAt = time.Now()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the native policy run did not succeed; last phase %q", runPhaseOf(run))
		}
		time.Sleep(liveTriggerPoll)
	}
	if run.Status.Outputs["allow"] != "true" {
		t.Fatalf("run outputs = %v, want allow=true", run.Status.Outputs)
	}

	var tr *v1alpha1.RunTrigger
	var triggeredAt time.Time
	deadline = time.Now().Add(2 * time.Minute)
	for {
		got, err := registry.ReadTrigger(ctx, triggerRef)
		if err == nil && got.Status.Phase == v1alpha1.RunTriggerTriggered {
			tr, triggeredAt = got, time.Now()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the trigger did not fire; last phase %q", triggerPhaseOf(got))
		}
		time.Sleep(liveTriggerPoll)
	}
	reportTriggerLatency(t, run, tr, triggeredAt.Sub(terminalAt))
	if tr.Status.LastRun != run.Name {
		t.Fatalf("trigger lastRun = %q, want %q", tr.Status.LastRun, run.Name)
	}
	if tr.Status.JobName == "" {
		t.Fatal("the trigger reported no job name")
	}
	jobRef := provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: tr.Status.JobName}
	deadline = time.Now().Add(4 * time.Minute)
	var job *v1alpha1.DenoJob
	for {
		job, err = registry.ReadJob(ctx, jobRef)
		if err == nil && (job.Status.Phase == v1alpha1.DenoJobSucceeded || job.Status.Phase == v1alpha1.DenoJobFailed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the deno job did not finish; last phase %q", jobPhaseOf(job))
		}
		time.Sleep(2 * time.Second)
	}
	if job.Status.Phase != v1alpha1.DenoJobSucceeded {
		t.Fatalf("job phase = %s, want Succeeded (retries=%d failed=%d)", job.Status.Phase, job.Status.Retries, job.Status.Failed)
	}
	if job.Status.Outputs["allow"] != "true" || job.Status.Outputs["httpStatus"] != "200" {
		t.Fatalf("job outputs = %v, want allow=true and httpStatus=200", job.Status.Outputs)
	}

	denoRun, err := registry.ReadRun(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: job.Status.RunName})
	if err != nil {
		t.Fatal(err)
	}
	if denoRun.Status.Outputs["allow"] != "true" {
		t.Fatalf("deno run outputs = %v", denoRun.Status.Outputs)
	}
	t.Logf("deno runtime lifecycle complete: run=%s allow=%s trigger=%s job=%s denoRun=%s http=%s",
		run.Status.Phase, run.Status.Outputs["allow"], v1alpha1.RunTriggerTriggered, job.Status.Phase,
		denoRun.Name, job.Status.Outputs["httpStatus"])
}

// ponytail: the API stores status timestamps at whole-second precision, so the provider-clock delta between a run's completionTime and the trigger's Complete condition cannot resolve anything below 1s. This polls both at TRIGGER_POLL (default 100ms) and stamps the test's own clock instead, which bounds the measurement error at about one poll interval at each end.
var liveTriggerPoll = liveEnvDuration("TRIGGER_POLL", 100*time.Millisecond)

func reportTriggerLatency(t *testing.T, run *v1alpha1.PolicyWorkflowRun, tr *v1alpha1.RunTrigger, observed time.Duration) {
	t.Helper()
	providerClock := "unavailable"
	if complete := meta.FindStatusCondition(tr.Status.Conditions, v1alpha1.ConditionComplete); complete != nil && run.Status.CompletionTime != nil {
		providerClock = complete.LastTransitionTime.Sub(run.Status.CompletionTime.Time).String()
	}
	t.Logf("trigger latency: run %s terminal -> trigger %s, observed %s locally (error up to %s); provider clock reads %s",
		run.Name, tr.Status.Phase, observed.Round(time.Millisecond), liveTriggerPoll, providerClock)
}

func triggerPhaseOf(tr *v1alpha1.RunTrigger) string {
	if tr == nil {
		return "<none>"
	}
	return string(tr.Status.Phase)
}

func newestRunForPod(runs []v1alpha1.PolicyWorkflowRun, podName string) *v1alpha1.PolicyWorkflowRun {
	var newest *v1alpha1.PolicyWorkflowRun
	for i := range runs {
		run := &runs[i]
		if run.Labels[v1alpha1.PolicyWorkflowPodLabel] != podName {
			continue
		}
		if newest == nil || run.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = run
		}
	}
	return newest
}

func runPhaseOf(run *v1alpha1.PolicyWorkflowRun) string {
	if run == nil {
		return "<none>"
	}
	return string(run.Status.Phase)
}

func jobPhaseOf(job *v1alpha1.DenoJob) string {
	if job == nil {
		return "<none>"
	}
	return string(job.Status.Phase)
}
