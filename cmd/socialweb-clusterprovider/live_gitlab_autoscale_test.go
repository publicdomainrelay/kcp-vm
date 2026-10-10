package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const (
	gitlabScaleJobCount = 5

	gitlabScaleDeadline = 45 * time.Minute

	gitlabScalePodWait = 6 * time.Minute
)

func gitlabScaleJobNames() []string {
	out := make([]string, 0, gitlabScaleJobCount)
	for i := 1; i <= gitlabScaleJobCount; i++ {
		out = append(out, fmt.Sprintf("scale-check-%d", i))
	}
	return out
}

func TestFiveGitLabJobPodsScaleToFiveNodesAndBackToZero(t *testing.T) {
	missing := []string{}
	for _, name := range []string{
		gitlabURLEnv, gitlabPATEnv, gitlabProjectEnv, gitlabRunnerTokenEnv, gitlabRunnerBinEnv,
		gitlabRefEnv,
	} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		livegate.Require(t, "unset: %s. This test derives five nodes from five real GitLab jobs and "+
			"then watches the cluster return to zero, so it needs a live GitLab, a project whose ref "+
			"carries the five scale-check jobs, a registered runner token and a real guest. It also "+
			"needs %s and %s set so the target can reach five and one job is one node. A skip here "+
			"is NOT a pass", strings.Join(missing, ", "), envScaleMaxNodes, envScalePodsPerNode)
	}

	gitlab := gitlabClient{
		url:     strings.TrimSuffix(os.Getenv(gitlabURLEnv), "/"),
		token:   os.Getenv(gitlabPATEnv),
		project: os.Getenv(gitlabProjectEnv),
		http:    &http.Client{Timeout: 60 * time.Second},
	}
	ref := strings.TrimSpace(os.Getenv(gitlabRefEnv))
	substrate := strings.TrimSpace(os.Getenv(fourstepSubstrateEnv))
	ran := 0
	for _, s := range fourstepSubstrates() {
		if substrate != "" && substrate != s {
			continue
		}
		ran++
		t.Run(s, func(t *testing.T) {
			gitlabAutoscaleRun(t, s, os.Getenv(gitlabRunnerBinEnv), os.Getenv(gitlabRunnerTokenEnv), gitlab, ref)
		})
	}
	if ran == 0 {
		livegate.Require(t, "%s is %q and this host is not one of %s",
			fourstepSubstrateEnv, substrate, strings.Join(fourstepSubstrates(), ", "))
	}
}

func gitlabAutoscaleRun(t *testing.T, substrate, runnerBin, runnerToken string, gitlab gitlabClient, ref string) {
	bins := fourstepRequirements(t, substrate)
	ctx, cancel := context.WithTimeout(context.Background(), gitlabScaleDeadline)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := fourstepConfig(t, bins)
	stack := fourstepBringUp(t, ctx, cfg, bins, log)

	ready := stack.waitReady(t, ctx)
	t.Logf("STEP 1 the control plane reached %s at %s", ready.Phase, ready.URL)

	client, err := stack.tenantClient()
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}
	gitlabNamespaceUp(t, ctx, client)

	credDir, host := gitlabScopedCredentialFiles(t, cfg, stack)
	gitlabScopedRBACUp(t, ctx, client)
	t.Logf("STEP 2 namespace %s exists and the runner carries its own scoped credential against %s",
		gitlabNamespace, host)

	kubeconfig := stack.kubeconfig(t)
	empty := stack.waitNodes(t, kubeconfig, 0)
	t.Logf("STEP 3 the cluster is at zero nodes before anything is applied: %q", strings.TrimSpace(empty))

	runner := gitlabStartJobRunner(t, runnerBin, runnerToken, gitlab.url, host, credDir)
	defer gitlabStopRunner(t, runner)

	gitlab.AwaitRunnerOnline(ctx)

	names := gitlabScaleJobNames()
	id, err := gitlab.TriggerPipelineOn(ctx, ref)
	if err != nil {
		t.Fatalf("triggering a pipeline for %s: %v\nrunner output:\n%s", ref, err, runner.output())
	}
	t.Logf("STEP 4 pipeline %d triggered for %s, carrying %s", id, ref, strings.Join(names, ", "))

	pods := gitlabWaitRunnerPods(t, ctx, client, gitlabScaleJobCount)
	t.Logf("STEP 5 %d job pods exist in %s: %s", len(pods), gitlabNamespace, strings.Join(podNames(pods), ", "))

	dec := stack.choose(t, ctx, scale.Provision)
	if dec.Nodes != gitlabScaleJobCount {
		t.Fatalf("the scale decision read %s with nodes=%d and %d job pods are pending or running "+
			"at one pod per node, so the target is %d. A target that does not follow the work is the "+
			"defect this test exists to catch (%s)",
			dec.Action, dec.Nodes, len(pods), gitlabScaleJobCount, dec.Reason)
	}
	t.Logf("STEP 6 the scale decision read %s with nodes=%d: %s", dec.Action, dec.Nodes, dec.Reason)

	joined := stack.waitNodes(t, kubeconfig, gitlabScaleJobCount)
	t.Logf("STEP 7 %d nodes joined on demand; `kubectl get nodes`:\n%s", gitlabScaleJobCount, joined)

	placed := gitlabWaitPlaced(t, ctx, client, gitlabScaleJobCount)
	nodes := map[string]bool{}
	for _, node := range placed {
		nodes[node] = true
	}
	if len(nodes) != gitlabScaleJobCount {
		t.Fatalf("%d job pods were placed across %d distinct nodes (%v), and the runner's "+
			"podAntiAffinity on %s with topologyKey kubernetes.io/hostname is what is supposed to make "+
			"that %d. Two jobs on one node is a target the cluster over-provisioned for, and the whole "+
			"point of measuring this is that the node count follows the work",
			len(placed), len(nodes), placed, gitlabSpreadLabelKey, gitlabScaleJobCount)
	}
	t.Logf("STEP 8 each job pod is on its own node: %s", describePlacement(placed))

	for _, name := range names {
		job := gitlab.AwaitJob(t, ctx, id, name)
		if job.status != "success" {
			t.Fatalf("job %s of pipeline %d finished %q, want success.\nall jobs:\n%s\ntrace tail:\n%s",
				name, id, job.status, gitlab.PipelineJobs(ctx, id),
				tailOf(gitlab.JobTrace(ctx, job.id), gitlabTraceTail))
		}
	}
	t.Logf("STEP 9 all %d scale-check jobs finished success", len(names))

	podsGone := gitlabWaitRunnerPods(t, ctx, client, 0)
	t.Logf("STEP 10 the job pods are gone (%d left), so nothing is asking for a node", len(podsGone))

	back := stack.waitNodes(t, kubeconfig, 0)
	t.Logf("STEP 11 the cluster is back at zero nodes; `kubectl get nodes` said %q", strings.TrimSpace(back))

	if _, err := stack.prov.Reconcile(ctx, stack.ref()); err != nil {
		t.Fatalf("the control plane stopped answering once the nodes were gone: %v -- a cluster that "+
			"returns to zero by dying has not scaled back, it has fallen over", err)
	}
	stillReady := stack.waitReady(t, ctx)
	t.Logf("STEP 12 the cluster is at zero nodes and its control plane is still %s at %s",
		stillReady.Phase, stillReady.URL)
}

func gitlabWaitRunnerPods(t *testing.T, ctx context.Context, client kubernetes.Interface, want int) []corev1.Pod {
	t.Helper()
	deadline := time.Now().Add(gitlabScalePodWait)
	last := 0
	for {
		pods := gitlabRunnerPods(t, ctx, client)
		last = len(pods)
		if last == want {
			return pods
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %s namespace held %d job pods and %d were wanted within %s. A pod that "+
				"never appears is a job nothing picked up, and one that never leaves is a job whose "+
				"node is never given back",
				gitlabNamespace, last, want, gitlabScalePodWait)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %d job pods: %v", want, ctx.Err())
		case <-time.After(fourstepReadyPoll):
		}
	}
}

func gitlabRunnerPods(t *testing.T, ctx context.Context, client kubernetes.Interface) []corev1.Pod {
	t.Helper()
	pods, err := client.CoreV1().Pods(gitlabNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing the job pods in %s: %v", gitlabNamespace, err)
	}
	out := make([]corev1.Pod, 0, len(pods.Items))
	for i := range pods.Items {
		if strings.HasPrefix(pods.Items[i].Name, "runner-") {
			out = append(out, pods.Items[i])
		}
	}
	return out
}

func gitlabWaitPlaced(t *testing.T, ctx context.Context, client kubernetes.Interface, want int) map[string]string {
	t.Helper()
	deadline := time.Now().Add(gitlabScalePodWait)
	for {
		placed := map[string]string{}
		for _, pod := range gitlabRunnerPods(t, ctx, client) {
			if pod.Spec.NodeName != "" {
				placed[pod.Name] = pod.Spec.NodeName
			}
		}
		if len(placed) == want {
			return placed
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d job pods reported a spec.nodeName within %s, so the placement the "+
				"anti-affinity is supposed to produce cannot be read. A pod the runner deletes when "+
				"its job ends has to be caught while it is alive", len(placed), want, gitlabScalePodWait)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %d placed job pods: %v", want, ctx.Err())
		case <-time.After(fourstepReadyPoll):
		}
	}
}

func podNames(pods []corev1.Pod) []string {
	out := make([]string, 0, len(pods))
	for i := range pods {
		out = append(out, pods[i].Name)
	}
	return out
}

func describePlacement(placed map[string]string) string {
	byNode := map[string][]string{}
	for pod, node := range placed {
		byNode[node] = append(byNode[node], pod)
	}
	out := make([]string, 0, len(byNode))
	for node, pods := range byNode {
		out = append(out, fmt.Sprintf("%s <- %s", node, strings.Join(pods, ",")))
	}
	return strings.Join(out, "; ")
}
