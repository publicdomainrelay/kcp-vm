//go:build linux

package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/provider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/execrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/policyclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestPodRunTTLReapsTheRunOnRealKCP(t *testing.T) {
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
		_, err := kubectlTry(t, kubeconfig, tenantCluster, "get", "policyworkflowpods")
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

	kubectlApply(t, kubeconfig, tenantCluster, ttlPodManifest("ttl-pod", 3))
	waitFor(t, "ttl pod Running", 60*time.Second, func() bool {
		w, err := registry.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "ttl-pod"})
		return err == nil && w.Status.Phase == v1alpha1.PolicyWorkflowPodRunning && w.Status.Endpoint != ""
	})
	w, err := registry.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: "ttl-pod"})
	if err != nil {
		t.Fatal(err)
	}

	if err := createRunWithRetry(ctx, registry, nativeRun(w, "ttl-pod-run")); err != nil {
		t.Fatalf("creating the run: %v", err)
	}

	var created *v1alpha1.PolicyWorkflowRun
	waitFor(t, "native run created", 60*time.Second, func() bool {
		runs, err := registry.ListWorkflowRuns(ctx, "root:runtime")
		if err != nil {
			return false
		}
		created = newestRunForPod(runs, "ttl-pod")
		return created != nil
	})
	if created.Spec.TTLSecondsAfterFinished != nil {
		t.Fatalf("run %q spec.ttlSecondsAfterFinished = %d, want it unset by the client",
			created.Name, *created.Spec.TTLSecondsAfterFinished)
	}
	t.Logf("native run %s sets no spec TTL; the pod's runTTLSecondsAfterFinished=3 resolves at submit time", created.Name)

	deadline := time.Now().Add(90 * time.Second)
	var completedAt time.Time
	var lastPhase v1alpha1.PolicyWorkflowPhase
	for {
		runs, err := registry.ListWorkflowRuns(ctx, "root:runtime")
		if err != nil {
			t.Fatal(err)
		}
		run := runNamed(runs, created.Name)
		if run == nil {
			break
		}
		lastPhase = run.Status.Phase
		if completedAt.IsZero() && (run.Status.Phase == v1alpha1.PolicyWorkflowSucceeded ||
			run.Status.Phase == v1alpha1.PolicyWorkflowFailed || run.Status.Phase == v1alpha1.PolicyWorkflowCancelled) {
			completedAt = time.Now()
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s was not reaped by its TTL; last phase %q", created.Name, lastPhase)
		}
		time.Sleep(300 * time.Millisecond)
	}

	if !completedAt.IsZero() && time.Since(completedAt) > 20*time.Second {
		t.Fatalf("run %s took %s to be reaped after completing, want <= 20s", created.Name, time.Since(completedAt))
	}
	t.Logf("run %s reaped by the pod's runTTLSecondsAfterFinished=3 (last phase seen %q)", created.Name, lastPhase)
}

func ttlPodManifest(name string, ttl int64) string {
	return fmt.Sprintf(`apiVersion: deno.computer/v1alpha1
kind: PolicyWorkflowPod
metadata:
  name: %s
  namespace: default
spec:
  policyEngine: policy-engine
  concurrencyPolicy: Forbid
  runTTLSecondsAfterFinished: %d
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
`, name, ttl)
}

func runNamed(runs []v1alpha1.PolicyWorkflowRun, name string) *v1alpha1.PolicyWorkflowRun {
	for i := range runs {
		if runs[i].Name == name {
			return &runs[i]
		}
	}
	return nil
}
