package provider

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/kcp"
)

type neverSyncingFactory struct{}

func (neverSyncingFactory) Start(stopCh <-chan struct{}) {}

func (neverSyncingFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
	<-stopCh
	return map[schema.GroupVersionResource]bool{gvrDenoPods: false}
}

type instantSyncingFactory struct{ gvr schema.GroupVersionResource }

func (instantSyncingFactory) Start(stopCh <-chan struct{}) {}

func (f instantSyncingFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
	return map[schema.GroupVersionResource]bool{f.gvr: true}
}

// ponytail: the bounded wait is the whole point. An implementation that waits
// on its first attempt without a deadline builds one round and never starts
// another, so the third attempt never arrives.
func TestTheInitialCacheSyncIsBoundedAndRetried(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	built := make(chan int, 32)
	var mu sync.Mutex
	attempts := 0
	build := func() (*informerRound, error) {
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()
		built <- attempt
		return &informerRound{factories: []informerFactory{neverSyncingFactory{}}, stop: make(chan struct{})}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = waitForCaches(ctx, build, 20*time.Millisecond, time.Millisecond, log)
	}()

	start := time.Now()
	guard := time.After(2 * time.Second)
	attempt := 0
	for attempt < 3 {
		select {
		case attempt = <-built:
		case <-guard:
			t.Fatalf("the bounded wait started %d attempts in %s; it did not abandon the first one within its deadline", attempt, time.Since(start))
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("three bounded attempts took %s; each deadline is 20ms", elapsed)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the bounded wait did not return after the context was cancelled")
	}
	if !strings.Contains(logs.String(), "retrying") {
		t.Fatalf("the bounded wait did not log the retry at info level: %q", logs.String())
	}
}

func TestASyncedCacheIsLoggedAndKeepsItsRound(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	round, err := waitForCaches(context.Background(), func() (*informerRound, error) {
		return &informerRound{factories: []informerFactory{instantSyncingFactory{gvr: gvrDenoPods}}, stop: make(chan struct{})}, nil
	}, time.Second, time.Millisecond, log)
	if err != nil {
		t.Fatal(err)
	}
	if round == nil {
		t.Fatal("a synced cache returned no round")
	}
	defer round.Close()
	if !strings.Contains(logs.String(), "have synced") {
		t.Fatalf("a synced cache was not logged at info level: %q", logs.String())
	}
}

func runObject(kind, name, lc, phase string, labels map[string]string) *unstructured.Unstructured {
	meta := map[string]any{
		"name":        name,
		"annotations": map[string]any{kcp.ClusterAnnotation: lc},
	}
	if labels != nil {
		raw := map[string]any{}
		for k, v := range labels {
			raw[k] = v
		}
		meta["labels"] = raw
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "deno.computer/v1alpha1",
		"kind":       kind,
		"metadata":   meta,
		"status":     map[string]any{"phase": phase},
	}}
}

func policyRunObject(name, lc, phase string, labels map[string]string) *unstructured.Unstructured {
	return runObject("PolicyWorkflowRun", name, lc, phase, labels)
}

func triggerObject(name, lc, pod string) *unstructured.Unstructured {
	return triggerObjectNS(name, lc, "", pod)
}

func triggerObjectNS(name, lc, ns, pod string) *unstructured.Unstructured {
	meta := map[string]any{
		"name":        name,
		"annotations": map[string]any{kcp.ClusterAnnotation: lc},
	}
	if ns != "" {
		meta["namespace"] = ns
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "deno.computer/v1alpha1",
		"kind":       "RunTrigger",
		"metadata":   meta,
		"spec":       map[string]any{"policyWorkflowPod": pod},
	}}
}

func triggerReader(t *testing.T, objects ...*unstructured.Unstructured) *cacheReader {
	t.Helper()
	indexer := newCacheStore()
	reader := newCacheReader()
	reader.add(workTrigger, indexer)
	for _, obj := range objects {
		if err := indexer.Add(obj); err != nil {
			t.Fatal(err)
		}
	}
	return reader
}

func newQueue(t *testing.T) workqueue.TypedRateLimitingInterface[workKey] {
	t.Helper()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[workKey]())
	t.Cleanup(queue.ShutDown)
	return queue
}

func drainKeys(queue workqueue.TypedRateLimitingInterface[workKey]) []workKey {
	var out []workKey
	for queue.Len() > 0 {
		key, shutdown := queue.Get()
		if shutdown {
			break
		}
		out = append(out, key)
		queue.Done(key)
	}
	return out
}

func TestTheTriggerPodIndexFindsTheTriggersThatNameThePod(t *testing.T) {
	reader := triggerReader(t,
		triggerObject("on-policy-allow", "root:runtime", "open-policy-pod"),
		triggerObject("on-policy-deny", "root:runtime", "other-pod"),
		triggerObject("on-policy-elsewhere", "root:other", "open-policy-pod"),
	)
	got := reader.triggerNamesForPod("root:runtime", "", "open-policy-pod")
	if len(got) != 1 || got[0] != "on-policy-allow" {
		t.Fatalf("triggerNamesForPod = %v, want [on-policy-allow]", got)
	}
}

// ponytail: the guard for the failure mode a namespace introduces. Every index was keyed logical-cluster plus name, so two same-named objects in different namespaces shared one index key and cacheReader.get returned whichever the index held first, which reconciles the wrong object rather than failing.
func TestTriggersOfTheSameNameInDifferentNamespacesDoNotCollide(t *testing.T) {
	reader := triggerReader(t,
		triggerObjectNS("on-allow", "root:runtime", "team-a", "policy-pod"),
		triggerObjectNS("on-allow", "root:runtime", "team-b", "policy-pod"),
	)
	a := reader.triggerNamesForPod("root:runtime", "team-a", "policy-pod")
	b := reader.triggerNamesForPod("root:runtime", "team-b", "policy-pod")
	if len(a) != 1 || a[0] != "on-allow" {
		t.Fatalf("team-a triggers = %v, want [on-allow]", a)
	}
	if len(b) != 1 || b[0] != "on-allow" {
		t.Fatalf("team-b triggers = %v, want [on-allow]", b)
	}
	if other := reader.triggerNamesForPod("root:runtime", "team-c", "policy-pod"); len(other) != 0 {
		t.Fatalf("a namespace with no triggers returned %v, want none", other)
	}
}

func TestAPolicyRunReachingATerminalPhaseWakesTheTriggersOfItsPod(t *testing.T) {
	for _, phase := range []string{"Succeeded", "Failed", "Cancelled"} {
		phase := phase
		t.Run(phase, func(t *testing.T) {
			reader := triggerReader(t,
				triggerObject("on-policy-allow", "root:runtime", "open-policy-pod"),
				triggerObject("on-policy-other", "root:runtime", "another-pod"),
			)
			queue := newQueue(t)
			labels := map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}
			enqueueWatchUpdate(workPolicyRun,
				policyRunObject("open-policy-pod-1", "root:runtime", "Running", labels),
				policyRunObject("open-policy-pod-1", "root:runtime", phase, labels), reader, queue)

			got := drainKeys(queue)
			want := workKey{kind: workTrigger, ref: Ref{LogicalCluster: "root:runtime", Name: "on-policy-allow"}}
			if len(got) != 2 || got[1] != want {
				t.Fatalf("queued keys = %v, want the run key then %v", got, want)
			}
		})
	}
}

func TestAPolicyRunThatIsNotTerminalDoesNotWakeTheTriggers(t *testing.T) {
	reader := triggerReader(t, triggerObject("on-policy-allow", "root:runtime", "open-policy-pod"))
	queue := newQueue(t)
	labels := map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}
	enqueueWatchUpdate(workPolicyRun,
		policyRunObject("open-policy-pod-1", "root:runtime", "Pending", labels),
		policyRunObject("open-policy-pod-1", "root:runtime", "Running", labels), reader, queue)

	got := drainKeys(queue)
	if len(got) != 1 || got[0].kind != workPolicyRun {
		t.Fatalf("queued keys = %v, want only the policy run key", got)
	}
}

func TestATerminalPolicyRunAppearingWakesTheTriggersOfItsPod(t *testing.T) {
	reader := triggerReader(t, triggerObject("on-policy-allow", "root:runtime", "open-policy-pod"))
	queue := newQueue(t)
	labels := map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}
	enqueueWatchObject(workPolicyRun, policyRunObject("open-policy-pod-1", "root:runtime", "Succeeded", labels), reader, queue)

	got := drainKeys(queue)
	if len(got) != 2 || got[1].kind != workTrigger {
		t.Fatalf("queued keys = %v, want the policy run key and the trigger key", got)
	}
}

func TestADenoRunNeverWakesATrigger(t *testing.T) {
	reader := triggerReader(t, triggerObject("on-policy-allow", "root:runtime", "open-policy-pod"))
	queue := newQueue(t)
	labels := map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}
	child := runObject("DenoRun", "on-policy-allow-open-policy-pod-1-1", "root:runtime", "Succeeded", labels)
	enqueueWatchObject(workRun, child, reader, queue)
	enqueueWatchUpdate(workRun, runObject("DenoRun", child.GetName(), "root:runtime", "Running", labels), child, reader, queue)

	for _, key := range drainKeys(queue) {
		if key.kind == workTrigger {
			t.Fatalf("a DenoRun woke %v; the trigger watches PolicyWorkflowRuns", key)
		}
	}
}

func TestATriggerWithoutAPodIsNotWoken(t *testing.T) {
	reader := triggerReader(t, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "deno.computer/v1alpha1",
		"kind":       "RunTrigger",
		"metadata": map[string]any{
			"name":        "no-pod",
			"annotations": map[string]any{kcp.ClusterAnnotation: "root:runtime"},
		},
		"spec": map[string]any{},
	}})
	queue := newQueue(t)
	labels := map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}
	enqueueWatchObject(workPolicyRun, policyRunObject("open-policy-pod-1", "root:runtime", "Succeeded", labels), reader, queue)

	for _, key := range drainKeys(queue) {
		if key.kind == workTrigger {
			t.Fatalf("a trigger with no pod reference was woken: %v", key)
		}
	}
}

func podObject(name, lc, ns, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "deno.computer/v1alpha1",
		"kind":       "DenoPod",
		"metadata": map[string]any{
			"name":        name,
			"namespace":   ns,
			"annotations": map[string]any{kcp.ClusterAnnotation: lc},
		},
		"status": map[string]any{"phase": phase},
	}}
}

// ponytail: the guard for the failure mode a second workspace introduces. Every
// object used to sit in the informer's own store, which keys by namespace and
// name, so two DenoPods named default/pds in two workspaces were one entry: the
// second Add evicted the first, its reads returned NotFound, and the evicted
// workload never reported ready.
func TestTwoWorkspacesKeepTheirSameNamedPods(t *testing.T) {
	store := newCacheStore()
	reader := newCacheReader()
	reader.add(workPod, store)

	alice := podObject("pds", "root:alice", "default", "Running")
	bob := podObject("pds", "root:bob", "default", "Running")
	for _, obj := range []*unstructured.Unstructured{alice, bob} {
		if err := store.Add(obj); err != nil {
			t.Fatal(err)
		}
	}

	if got := len(reader.allPods()); got != 2 {
		t.Fatalf("the cache holds %d pods, want both workspaces' default/pds", got)
	}
	for _, lc := range []string{"root:alice", "root:bob"} {
		pod, err := reader.ReadPod(context.Background(), Ref{LogicalCluster: lc, Namespace: "default", Name: "pds"})
		if err != nil {
			t.Fatalf("ReadPod(%s default/pds): %v", lc, err)
		}
		if got := pod.Annotations[kcp.ClusterAnnotation]; got != lc {
			t.Fatalf("ReadPod(%s default/pds) returned the pod of %s", lc, got)
		}
	}

	if err := store.Update(bob); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadPod(context.Background(), Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "pds"}); err != nil {
		t.Fatalf("an event for root:bob evicted root:alice's pod: %v", err)
	}
}
