package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/policy"
)

func TestNewResolvesBundledActionsDirAgainstTheWorkingDir(t *testing.T) {
	p, err := New(Options{Registry: newFakeRuntime(), RestConfig: &rest.Config{Host: "https://kcp"}, BundledActionsDir: "some/actions"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("some/actions")
	if err != nil {
		t.Fatal(err)
	}
	if p.opts.BundledActionsDir != want {
		t.Fatalf("BundledActionsDir = %q, want %q", p.opts.BundledActionsDir, want)
	}
}

func rawWorkflow() runtime.RawExtension {
	return runtime.RawExtension{Raw: []byte(`{"name":"x"}`)}
}

func TestStatusPatchForcesManagedFieldsPresent(t *testing.T) {
	body, err := statusPatch(v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	status := decoded["status"]
	for _, key := range []string{"phase", "runID", "active", "succeeded", "failed", "retries",
		"exitStatus", "startTime", "completionTime", "outputs", "conditions"} {
		if _, ok := status[key]; !ok {
			t.Fatalf("status field %q missing: %v", key, status)
		}
	}
	if status["active"] != float64(0) {
		t.Fatalf("active = %v, want a present zero so a merge patch clears it", status["active"])
	}
}

type fakePolicyClient struct {
	mu sync.Mutex

	seq int

	tasks map[string]policy.Task

	fail bool
}

func newFakePolicyClient(fail bool) *fakePolicyClient {
	return &fakePolicyClient{tasks: map[string]policy.Task{}, fail: fail}
}

func (c *fakePolicyClient) Submit(_ context.Context, endpoint string, workflow []byte, inputs map[string]string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("no endpoint")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("task-%d", c.seq)
	if c.fail {
		c.tasks[id] = policy.Task{State: "failed", ExitStatus: "failure"}
	} else {
		c.tasks[id] = policy.Task{State: "succeeded", ExitStatus: "success", Outputs: map[string]string{"allow": "true"}}
	}
	return id, nil
}

func (c *fakePolicyClient) Status(_ context.Context, endpoint, taskID string) (policy.Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tasks[taskID], nil
}

type fakeStore struct {
	inst *v1alpha1.PolicyWorkflowRun

	writes []v1alpha1.PolicyWorkflowRunStatus

	deleted bool

	unfinalized bool
}

func (f *fakeStore) Read(context.Context, Ref) (*v1alpha1.PolicyWorkflowRun, error) {
	return f.inst.DeepCopy(), nil
}

func (f *fakeStore) WriteStatus(_ context.Context, _ Ref, st v1alpha1.PolicyWorkflowRunStatus) error {
	f.writes = append(f.writes, st)
	f.inst.Status = st
	return nil
}

func (f *fakeStore) Delete(context.Context, Ref) error {
	f.deleted = true
	return nil
}

func (f *fakeStore) RemoveFinalizer(context.Context, Ref) error {
	f.unfinalized = true
	return nil
}

func newProvider(t *testing.T, store Instances, client policy.Client) *Provider {
	t.Helper()
	p, err := New(Options{Registry: store, PolicyClient: client, RestConfig: &rest.Config{Host: "https://kcp"}, WriteStatus: true})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func reconcileOnce(t *testing.T, p *Provider) {
	t.Helper()
	if _, err := p.Reconcile(context.Background(), Ref{LogicalCluster: "root:demo", Name: "demo"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func newRun(spec v1alpha1.PolicyWorkflowRunSpec) *fakeStore {
	if spec.EngineEndpoint == "" {
		spec.EngineEndpoint = "http://127.0.0.1:9"
	}
	return &fakeStore{inst: &v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Generation: 1},
		Spec:       spec,
	}}
}

func TestReconcileStartsThenCompletes(t *testing.T) {
	store := newRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()})
	p := newProvider(t, store, newFakePolicyClient(false))

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowRunning || store.inst.Status.RunID != "task-1" {
		t.Fatalf("after start: phase=%s runID=%s", store.inst.Status.Phase, store.inst.Status.RunID)
	}

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowSucceeded {
		t.Fatalf("after observe: phase=%s", store.inst.Status.Phase)
	}
	if store.inst.Status.Outputs["allow"] != "true" {
		t.Fatalf("outputs = %v", store.inst.Status.Outputs)
	}
}

func TestReconcileRetriesUntilBackoffLimit(t *testing.T) {
	limit := int32(1)
	store := newRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), BackoffLimit: &limit})
	p := newProvider(t, store, newFakePolicyClient(true))

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowRunning {
		t.Fatalf("start: phase=%s", store.inst.Status.Phase)
	}

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowPending || store.inst.Status.Retries != 1 {
		t.Fatalf("first failure: phase=%s retries=%d", store.inst.Status.Phase, store.inst.Status.Retries)
	}
	if store.inst.Status.RunID != "" {
		t.Fatalf("runID = %q, want cleared for retry", store.inst.Status.RunID)
	}

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowRunning || store.inst.Status.RunID != "task-2" {
		t.Fatalf("retry start: phase=%s runID=%s", store.inst.Status.Phase, store.inst.Status.RunID)
	}

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowFailed {
		t.Fatalf("second failure: phase=%s", store.inst.Status.Phase)
	}
}

func TestReconcileDeletionUnfinalizes(t *testing.T) {
	store := newRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow()})
	now := metav1.Now()
	store.inst.DeletionTimestamp = &now
	store.inst.Status = v1alpha1.PolicyWorkflowRunStatus{
		Phase: v1alpha1.PolicyWorkflowRunning, RunID: "task-1",
	}
	p := newProvider(t, store, newFakePolicyClient(false))

	reconcileOnce(t, p)
	if !store.unfinalized {
		t.Fatal("deletion should release the finalizer")
	}
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowCancelled {
		t.Fatalf("phase = %s, want Cancelled on deletion", store.inst.Status.Phase)
	}
}

func TestReconcileCancelWhileRunningMarksCancelled(t *testing.T) {
	store := newRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), Cancel: true})
	store.inst.Status = v1alpha1.PolicyWorkflowRunStatus{
		Phase: v1alpha1.PolicyWorkflowRunning, RunID: "task-1", Active: 1,
	}
	p := newProvider(t, store, newFakePolicyClient(false))

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowCancelled {
		t.Fatalf("phase = %s, want Cancelled", store.inst.Status.Phase)
	}
	if store.inst.Status.RunID != "" {
		t.Fatalf("runID = %q, want cleared so polling stops", store.inst.Status.RunID)
	}
	if !store.unfinalized {
		t.Fatal("cancel should release the finalizer")
	}
}

func TestReconcileCancelAfterTerminalIsANoOp(t *testing.T) {
	store := newRun(v1alpha1.PolicyWorkflowRunSpec{Workflow: rawWorkflow(), Cancel: true})
	store.inst.Status = v1alpha1.PolicyWorkflowRunStatus{
		Phase: v1alpha1.PolicyWorkflowSucceeded, Succeeded: 1, Outputs: map[string]string{"allow": "true"},
	}
	p := newProvider(t, store, newFakePolicyClient(false))

	reconcileOnce(t, p)
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowSucceeded {
		t.Fatalf("phase = %s, want Succeeded (cancel is a no-op after terminal)", store.inst.Status.Phase)
	}
	if store.unfinalized {
		t.Fatal("cancel after terminal must not touch the finalizer")
	}
}
