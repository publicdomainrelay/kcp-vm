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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

const (
	gitlabNamespace = "gitlab-runner"

	gitlabPodName = "gitlab-runner-job-1"
)

func TestAGitLabJobPodIsPlacedOnAProvisionedNode(t *testing.T) {
	want := strings.TrimSpace(os.Getenv(fourstepSubstrateEnv))
	ran := 0
	for _, substrate := range fourstepSubstrates() {
		if want != "" && want != substrate {
			continue
		}
		ran++
		t.Run(substrate, func(t *testing.T) { gitlabRun(t, substrate) })
	}
	if ran != 0 {
		return
	}
	livegate.Require(t, "%s is %q and this host is %s, which cannot run that substrate: this test "+
		"derives the placement of a GitLab Runner's job pod from the scale decision over a real "+
		"node, so it must name a substrate this host supports: %s",
		fourstepSubstrateEnv, want, hostos.Name(), strings.Join(fourstepSubstrates(), ", "))
}

func gitlabRun(t *testing.T, substrate string) {
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

	gitlabPodUp(t, ctx, client, gitlabPodName, stack.bins.tenantImage)
	t.Logf("STEP 3 the GitLab Runner job pod shape is applied in %s: one helper container and one "+
		"build container, RestartPolicy Never, labelled job=gitlab-ci with no control-plane annotation",
		gitlabNamespace)

	dec := stack.choose(t, ctx, scale.Provision)
	if dec.Nodes != 1 {
		t.Fatalf("the provisioning decision counted %d nodes, want 1: one GitLab job pod is one "+
			"node, and a second provision here is a second node (ADR 0014: idempotency is "+
			"per-cluster)", dec.Nodes)
	}
	t.Log("STEP 4 the scale decision read Provision with nodes=1")

	joined := stack.waitNodes(t, kubeconfig, 1)
	stack.waitNodeReady(t, ctx)
	t.Logf("STEP 5 the node joined on demand; `kubectl get nodes`:\n%s", joined)

	gitlabWaitSucceeded(t, ctx, client)
	t.Log("STEP 6 the GitLab Runner job pod reached Succeeded")

	scheduled := gitlabNodeName(t, ctx, client)
	want, err := nodeprovider.NodeName(fourstepCluster, nodeID)
	if err != nil {
		t.Fatalf("nodeprovider.NodeName: %v", err)
	}
	if scheduled != want {
		t.Fatalf("the GitLab Runner job pod was scheduled on %q, want %q: the node the scale "+
			"decision provisioned for it. A pod that ran anywhere else is running on a node this "+
			"cluster did not place, so the RFP flow is not what put it there", scheduled, want)
	}
	t.Logf("STEP 7 the GitLab Runner job pod ran on %s, the node the scale decision provisioned",
		scheduled)

	if err := client.CoreV1().Pods(gitlabNamespace).Delete(ctx, gitlabPodName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("removing the GitLab Runner job pod: %v", err)
	}
	stack.choose(t, ctx, scale.Teardown)
	stack.waitNodes(t, kubeconfig, 0)
	t.Log("STEP 8 the node is gone; `kubectl get nodes` is empty again")

	if _, err := stack.prov.Reconcile(ctx, stack.ref()); err != nil {
		t.Fatalf("STEP 9 the control plane stopped answering once the node was gone: %v -- a "+
			"cluster that returns to zero by dying has not returned to zero", err)
	}
	stillReady := stack.waitReady(t, ctx)
	t.Logf("STEP 9 the cluster is back at zero nodes and its control plane is still %s at %s",
		stillReady.Phase, stillReady.URL)
}

func gitlabNamespaceUp(t *testing.T, ctx context.Context, client kubernetes.Interface) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: gitlabNamespace}}
	if _, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return
		}
		t.Fatalf("creating namespace %s for the GitLab Runner: %v. A GitLab Runner's kubernetes "+
			"executor needs namespaces:create, or the namespace pre-created, so its job pod has "+
			"somewhere to run", gitlabNamespace, err)
	}
}

func gitlabPodUp(t *testing.T, ctx context.Context, client kubernetes.Interface, name, image string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err := client.CoreV1().Pods(gitlabNamespace).Create(ctx, gitlabJobPod(name, image), metav1.CreateOptions{})
		if err == nil {
			return
		}
		if !apierrors.IsNotFound(err) && !strings.Contains(err.Error(), "serviceaccount") {
			t.Fatalf("applying the GitLab Runner's job pod shape in %s: %v. A GitLab Runner's "+
				"kubernetes executor creates exactly this pod -- one helper container and one build "+
				"container, RestartPolicy Never -- so it needs pods:create in this namespace",
				gitlabNamespace, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the namespace %s never admitted the GitLab Runner's job pod: %v. k3s's "+
				"controller-manager creates the default ServiceAccount on its first sync, so a pod "+
				"applied immediately after the namespace races it", gitlabNamespace, err)
		}
		time.Sleep(fourstepReadyPoll)
	}
}

func gitlabJobPod(name, image string) *corev1.Pod {
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("64Mi"),
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gitlabNamespace,
			Labels: map[string]string{
				"job":                      "gitlab-ci",
				"runner.gitlab.com/job-id": "1",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:      "build",
				Image:     image,
				Command:   []string{"sh", "-c", "echo build on $(hostname); sleep 3"},
				Resources: corev1.ResourceRequirements{Requests: requests},
			}, {
				Name:      "helper",
				Image:     image,
				Command:   []string{"sh", "-c", "echo helper on $(hostname)"},
				Resources: corev1.ResourceRequirements{Requests: requests},
			}},
		},
	}
}

func gitlabWaitSucceeded(t *testing.T, ctx context.Context, client kubernetes.Interface) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	for {
		pod, err := client.CoreV1().Pods(gitlabNamespace).Get(ctx, gitlabPodName, metav1.GetOptions{})
		if err == nil && pod.Status.Phase == corev1.PodSucceeded {
			return
		}
		if err == nil && pod.Status.Phase == corev1.PodFailed {
			t.Fatalf("the GitLab Runner job pod failed: %s\nlast events:\n%s",
				gitlabPodStatus(pod), gitlabPodEvents(t, ctx, client, gitlabPodName))
		}
		if time.Now().After(deadline) {
			status := fmt.Sprintf("(reading the pod: %v)", err)
			if err == nil {
				status = gitlabPodStatus(pod)
			}
			t.Fatalf("the GitLab Runner job pod never reached Succeeded within 6m; last status %s"+
				"\nlast events:\n%s", status, gitlabPodEvents(t, ctx, client, gitlabPodName))
		}
		time.Sleep(fourstepReadyPoll)
	}
}

func gitlabNodeName(t *testing.T, ctx context.Context, client kubernetes.Interface) string {
	t.Helper()
	pod, err := client.CoreV1().Pods(gitlabNamespace).Get(ctx, gitlabPodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the GitLab Runner job pod's placement: %v", err)
	}
	if pod.Spec.NodeName == "" {
		t.Fatalf("the GitLab Runner job pod reports no spec.nodeName, so it was never placed on a "+
			"node this cluster provisioned; status %s", gitlabPodStatus(pod))
	}
	return pod.Spec.NodeName
}

func gitlabPodStatus(pod *corev1.Pod) string {
	parts := []string{fmt.Sprintf("phase=%s reason=%q message=%q",
		pod.Status.Phase, pod.Status.Reason, pod.Status.Message)}
	for _, cs := range pod.Status.ContainerStatuses {
		parts = append(parts, fmt.Sprintf("%s ready=%t restarts=%d state=%+v",
			cs.Name, cs.Ready, cs.RestartCount, cs.State))
	}
	return strings.Join(parts, "; ")
}

func gitlabPodEvents(t *testing.T, ctx context.Context, client kubernetes.Interface, name string) string {
	t.Helper()
	events, err := client.CoreV1().Events(gitlabNamespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name,
	})
	if err != nil {
		return fmt.Sprintf("(listing events: %v)", err)
	}
	lines := []string{}
	for i := range events.Items {
		e := events.Items[i]
		lines = append(lines, fmt.Sprintf("%s %s %s", e.LastTimestamp, e.Reason, e.Message))
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	if len(lines) == 0 {
		return "(no events)"
	}
	return strings.Join(lines, "\n")
}
