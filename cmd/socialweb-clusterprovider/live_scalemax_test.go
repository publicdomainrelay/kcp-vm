package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

func gitlabThreePodNames() []string {
	return []string{
		"gitlab-runner-job-1",
		"gitlab-runner-job-2",
		"gitlab-runner-job-3",
	}
}

func TestThreeGitLabJobPodsScaleToThreeNodes(t *testing.T) {
	want := strings.TrimSpace(os.Getenv(fourstepSubstrateEnv))
	ran := 0
	for _, substrate := range fourstepSubstrates() {
		if want != "" && want != substrate {
			continue
		}
		ran++
		t.Run(substrate, func(t *testing.T) { threePodsRun(t, substrate) })
	}
	if ran != 0 {
		return
	}
	livegate.Require(t, "%s is %q and this host is %s, which cannot run that substrate: this test "+
		"derives three nodes from the scale decision over three real GitLab-shaped pods, so it must "+
		"name a substrate this host supports: %s",
		fourstepSubstrateEnv, want, hostos.Name(), strings.Join(fourstepSubstrates(), ", "))
}

func threePodsRun(t *testing.T, substrate string) {
	bins := fourstepRequirements(t, substrate)
	ctx, cancel := context.WithTimeout(context.Background(), fourstepDeadline)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := fourstepConfig(t, bins)
	stack := fourstepBringUp(t, ctx, cfg, bins, log)

	ready := stack.waitReady(t, ctx)
	t.Logf("STEP 1 the control plane reached %s at %s with zero nodes", ready.Phase, ready.URL)

	kubeconfig := stack.kubeconfig(t)
	stack.waitNodes(t, kubeconfig, 0)
	t.Log("STEP 1 `kubectl get nodes` is empty")

	client, err := stack.tenantClient()
	if err != nil {
		t.Fatalf("the tenant client: %v", err)
	}

	gitlabNamespaceUp(t, ctx, client)
	t.Logf("STEP 2 namespace %s exists", gitlabNamespace)

	names := gitlabThreePodNames()
	for _, name := range names {
		gitlabPodUp(t, ctx, client, name, stack.bins.tenantImage)
	}
	t.Logf("STEP 3 three GitLab Runner job pod shapes are applied in %s: each with one helper "+
		"container and one build container, RestartPolicy Never, labelled job=gitlab-ci with no "+
		"control-plane annotation: %s", gitlabNamespace, strings.Join(names, ", "))

	dec := stack.choose(t, ctx, scale.Provision)
	if dec.Nodes != 3 {
		t.Fatalf("the provisioning decision counted %d nodes, want 3: three GitLab job pods at one "+
			"pod per node is a target of three, and a decision of three that produces one node is "+
			"exactly the defect this test exists to catch (%s)", dec.Nodes, dec.Reason)
	}
	t.Logf("STEP 4 the scale decision read Provision with nodes=3: %s", dec.Reason)

	joined := stack.waitNodes(t, kubeconfig, 3)
	t.Logf("STEP 5 three nodes joined on demand; `kubectl get nodes`:\n%s", joined)

	gitlabPodsWaitSucceeded(t, ctx, client, names)
	t.Log("STEP 6 all three GitLab Runner job pods reached Succeeded")

	placed := gitlabPodsNodeNames(t, ctx, client, names)
	t.Logf("STEP 7 each GitLab Runner job pod was placed; spec.nodeName per pod: %s",
		strings.Join(placed, ", "))

	for _, name := range names {
		if err := client.CoreV1().Pods(gitlabNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("removing the GitLab Runner job pod %s: %v", name, err)
		}
	}
	stack.choose(t, ctx, scale.Teardown)
	stack.waitNodes(t, kubeconfig, 0)
	t.Log("STEP 8 the nodes are gone; `kubectl get nodes` is empty again")

	if _, err := stack.prov.Reconcile(ctx, stack.ref()); err != nil {
		t.Fatalf("STEP 9 the control plane stopped answering once the nodes were gone: %v -- a "+
			"cluster that returns to zero by dying has not returned to zero", err)
	}
	stillReady := stack.waitReady(t, ctx)
	t.Logf("STEP 9 the cluster is back at zero nodes and its control plane is still %s at %s",
		stillReady.Phase, stillReady.URL)
}

func gitlabPodsWaitSucceeded(t *testing.T, ctx context.Context, client kubernetes.Interface, names []string) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	for {
		all := true
		report := make([]string, 0, len(names))
		for _, name := range names {
			pod, err := client.CoreV1().Pods(gitlabNamespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				all = false
				report = append(report, fmt.Sprintf("%s (reading the pod: %v)", name, err))
				continue
			}
			if pod.Status.Phase == corev1.PodFailed {
				t.Fatalf("the GitLab Runner job pod %s failed: %s\nlast events:\n%s",
					name, gitlabPodStatus(pod), gitlabPodEvents(t, ctx, client, name))
			}
			if pod.Status.Phase != corev1.PodSucceeded {
				all = false
			}
			report = append(report, fmt.Sprintf("%s %s", name, gitlabPodStatus(pod)))
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the three GitLab Runner job pods never all reached Succeeded within 6m; last "+
				"status:\n%s\nlast events:\n%s", strings.Join(report, "\n"),
				gitlabPodsEvents(t, ctx, client, names))
		}
		time.Sleep(fourstepReadyPoll)
	}
}

func gitlabPodsEvents(t *testing.T, ctx context.Context, client kubernetes.Interface, names []string) string {
	t.Helper()
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("%s:\n%s", name, gitlabPodEvents(t, ctx, client, name)))
	}
	return strings.Join(lines, "\n")
}

func gitlabPodsNodeNames(t *testing.T, ctx context.Context, client kubernetes.Interface, names []string) []string {
	t.Helper()
	out := make([]string, 0, len(names))
	for _, name := range names {
		pod, err := client.CoreV1().Pods(gitlabNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading the GitLab Runner job pod %s's placement: %v", name, err)
		}
		if pod.Spec.NodeName == "" {
			t.Fatalf("the GitLab Runner job pod %s reports no spec.nodeName, so it was never placed "+
				"on a node this cluster provisioned; status %s", name, gitlabPodStatus(pod))
		}
		out = append(out, fmt.Sprintf("%s=%s", name, pod.Spec.NodeName))
	}
	return out
}
