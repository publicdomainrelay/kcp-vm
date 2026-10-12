//go:build linux

package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/provider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/queue"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/execrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/policyclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestMaxConcurrentRunAdmissionOnRealKCP(t *testing.T) {
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

	pods := []struct {
		name          string
		maxConcurrent *int32
		cap           int
	}{
		{"cap-pod-1", int32Ptr(1), 1},
		{"cap-pod-2", int32Ptr(2), 2},
		{"cap-pod-3", int32Ptr(3), 3},
		{"cap-pod-unset", nil, 3},
	}

	var manifest strings.Builder
	for _, pod := range pods {
		manifest.WriteString(maxConcurrentPodManifest(pod.name, pod.maxConcurrent))
		manifest.WriteString("---\n")
	}
	kubectlApply(t, kubeconfig, tenantCluster, manifest.String())

	for _, pod := range pods {
		pod := pod
		waitFor(t, pod.name+" Running", 60*time.Second, func() bool {
			w, err := registry.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: pod.name})
			return err == nil && w.Status.Phase == v1alpha1.PolicyWorkflowPodRunning && w.Status.Endpoint != ""
		})
	}

	for _, pod := range pods {
		w, err := registry.ReadWorkflowPod(ctx, provider.Ref{LogicalCluster: "root:runtime", Namespace: "default", Name: pod.name})
		if err != nil {
			t.Fatal(err)
		}

		accepted := make([]string, 0, 3)
		for i := 0; i < 3; i++ {
			run := nativeRun(w, fmt.Sprintf("%s-%d", pod.name, i))
			if err := createRunWithRetry(ctx, registry, run); err != nil {
				t.Fatalf("%s create %d was rejected: %v", pod.name, i+1, err)
			}
			accepted = append(accepted, run.Name)
		}
		if len(accepted) != 3 {
			t.Fatalf("%s accepted %d of 3 creates, want all 3 queued and none rejected", pod.name, len(accepted))
		}

		peakRunning := 0
		queued := 0
		deadline := time.Now().Add(90 * time.Second)
		for {
			runs, err := registry.ListWorkflowRuns(ctx, "root:runtime")
			if err != nil {
				t.Fatal(err)
			}
			running, succeeded, atCapacity := summarizePodRuns(runs, pod.name)
			if running > peakRunning {
				peakRunning = running
			}
			if atCapacity > queued {
				queued = atCapacity
			}
			if succeeded == 3 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s ran %d of the 3 accepted runs; rows %s", pod.name, succeeded, podRunSummary(runs, pod.name))
			}
			time.Sleep(200 * time.Millisecond)
		}

		if peakRunning > pod.cap {
			t.Fatalf("%s ran %d policy runs at once, cap %d", pod.name, peakRunning, pod.cap)
		}
		if pod.cap < 3 && queued == 0 {
			t.Fatalf("%s accepted 3 creates at cap %d but never held a run Pending with a %s=False %s condition",
				pod.name, pod.cap, v1alpha1.ConditionComplete, queue.ReasonAtCapacity)
		}
		capDisplay := "unset"
		if pod.maxConcurrent != nil {
			capDisplay = fmt.Sprint(*pod.maxConcurrent)
		}
		t.Logf("%s concurrencyPolicy=Allow maxConcurrent=%s accepted=%d/3 peakRunning=%d peakQueuedAtCapacity=%d drained=3/3",
			pod.name, capDisplay, len(accepted), peakRunning, queued)
	}
}

func maxConcurrentPodManifest(name string, maxConcurrent *int32) string {
	capLine := ""
	if maxConcurrent != nil {
		capLine = fmt.Sprintf("\n  maxConcurrent: %d", *maxConcurrent)
	}
	return fmt.Sprintf(`apiVersion: deno.computer/v1alpha1
kind: PolicyWorkflowPod
metadata:
  name: %s
  namespace: default
spec:
  policyEngine: policy-engine
  concurrencyPolicy: Allow%s
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
`, name, capLine)
}

func int32Ptr(v int32) *int32 { return &v }

func summarizePodRuns(runs []v1alpha1.PolicyWorkflowRun, podName string) (running, succeeded, atCapacity int) {
	for i := range runs {
		run := runs[i]
		if run.Labels[v1alpha1.PolicyWorkflowPodLabel] != podName {
			continue
		}
		switch run.Status.Phase {
		case v1alpha1.PolicyWorkflowRunning:
			running++
		case v1alpha1.PolicyWorkflowSucceeded:
			succeeded++
		case v1alpha1.PolicyWorkflowPending:
			if completeCondition(run.Status.Conditions) == string(metav1.ConditionFalse)+"/"+queue.ReasonAtCapacity {
				atCapacity++
			}
		}
	}
	return running, succeeded, atCapacity
}

func podRunSummary(runs []v1alpha1.PolicyWorkflowRun, podName string) string {
	var rows []string
	for i := range runs {
		run := runs[i]
		if run.Labels[v1alpha1.PolicyWorkflowPodLabel] != podName {
			continue
		}
		rows = append(rows, fmt.Sprintf("%s phase=%s active=%d complete=%s", run.Name, run.Status.Phase, run.Status.Active, completeCondition(run.Status.Conditions)))
	}
	return strings.Join(rows, "; ")
}

func completeCondition(conditions []metav1.Condition) string {
	for i := range conditions {
		if conditions[i].Type == v1alpha1.ConditionComplete {
			return string(conditions[i].Status) + "/" + conditions[i].Reason
		}
	}
	return "none"
}
