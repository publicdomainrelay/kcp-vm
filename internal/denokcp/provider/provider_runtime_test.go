package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/memoryrunner"
)

func TestPodStatusPatchForcesManagedFieldsPresent(t *testing.T) {
	body, err := podStatusPatch(v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning})
	if err != nil {
		t.Fatal(err)
	}
	assertStatusFields(t, body, "phase", "runID", "restarts", "message", "exitCode", "ready", "address",
		"listeners", "startTime", "completionTime", "outputs", "conditions")
}

func TestPodStatusPatchCarriesTheReportedListeners(t *testing.T) {
	want := []v1alpha1.DenoPodListener{
		{Name: "tcp", Kind: "tcp", Address: "127.0.0.1:40305"},
		{Name: "relay", Kind: "relay", Address: "https://did-key-x.relay.localhost"},
	}
	body, err := podStatusPatch(v1alpha1.DenoPodStatus{Phase: v1alpha1.DenoPodRunning, Listeners: want})
	if err != nil {
		t.Fatal(err)
	}
	var patch struct {
		Status v1alpha1.DenoPodStatus `json:"status"`
	}
	if err := json.Unmarshal(body, &patch); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(patch.Status.Listeners, want) {
		t.Fatalf("the status patch carries listeners %+v, want %+v", patch.Status.Listeners, want)
	}
}

func TestRunStatusPatchForcesManagedFieldsPresent(t *testing.T) {
	body, err := runStatusPatch(v1alpha1.DenoRunStatus{Phase: v1alpha1.DenoRunSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	assertStatusFields(t, body, "phase", "runID", "message", "exitCode", "retries",
		"startTime", "completionTime", "outputs", "conditions")
}

func TestJobStatusPatchForcesManagedFieldsPresent(t *testing.T) {
	body, err := jobStatusPatch(v1alpha1.DenoJobStatus{Phase: v1alpha1.DenoJobSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	assertStatusFields(t, body, "phase", "runName", "runs", "active", "ready", "succeeded", "failed", "retries",
		"exitCode", "startTime", "completionTime", "outputs", "conditions")
}

func TestTriggerStatusPatchForcesManagedFieldsPresent(t *testing.T) {
	body, err := triggerStatusPatch(v1alpha1.RunTriggerStatus{Phase: v1alpha1.RunTriggerTriggered})
	if err != nil {
		t.Fatal(err)
	}
	assertStatusFields(t, body, "phase", "matched", "jobName", "lastRun", "conditions")
}

func assertStatusFields(t *testing.T, body []byte, keys ...string) {
	t.Helper()
	var decoded map[string]map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	status := decoded["status"]
	for _, key := range keys {
		if _, ok := status[key]; !ok {
			t.Fatalf("status field %q missing: %v", key, status)
		}
	}
}

type fakeRuntime struct {
	mu sync.Mutex

	pods map[Ref]*v1alpha1.DenoPod

	runs map[Ref]*v1alpha1.DenoRun

	jobs map[Ref]*v1alpha1.DenoJob

	triggers map[Ref]*v1alpha1.RunTrigger

	engines map[Ref]*v1alpha1.PolicyEngine

	workflowPods map[Ref]*v1alpha1.PolicyWorkflowPod

	pwi map[Ref]*v1alpha1.PolicyWorkflowRun

	openBaoStatus map[Ref]v1alpha1.OpenBaoStatus

	openBaoFinalizers map[Ref][]string

	openBaos []v1alpha1.OpenBao
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		pods:         map[Ref]*v1alpha1.DenoPod{},
		runs:         map[Ref]*v1alpha1.DenoRun{},
		jobs:         map[Ref]*v1alpha1.DenoJob{},
		triggers:     map[Ref]*v1alpha1.RunTrigger{},
		engines:      map[Ref]*v1alpha1.PolicyEngine{},
		workflowPods: map[Ref]*v1alpha1.PolicyWorkflowPod{},
		pwi:          map[Ref]*v1alpha1.PolicyWorkflowRun{},

		openBaoStatus:     map[Ref]v1alpha1.OpenBaoStatus{},
		openBaoFinalizers: map[Ref][]string{},
	}
}

func (f *fakeRuntime) ListPods(context.Context, string) ([]v1alpha1.DenoPod, error) {
	return nil, nil
}
func (f *fakeRuntime) ListRuns(_ context.Context, lc string) ([]v1alpha1.DenoRun, error) {
	out := make([]v1alpha1.DenoRun, 0, len(f.runs))
	for ref, run := range f.runs {
		if ref.LogicalCluster == lc {
			out = append(out, *run.DeepCopy())
		}
	}
	return out, nil
}
func (f *fakeRuntime) ListJobs(context.Context, string) ([]v1alpha1.DenoJob, error) {
	return nil, nil
}
func (f *fakeRuntime) ListTriggers(context.Context, string) ([]v1alpha1.RunTrigger, error) {
	return nil, nil
}

func (f *fakeRuntime) ListEngines(context.Context, string) ([]v1alpha1.PolicyEngine, error) {
	return nil, nil
}

func (f *fakeRuntime) ListWorkflowPods(context.Context, string) ([]v1alpha1.PolicyWorkflowPod, error) {
	return nil, nil
}

func (f *fakeRuntime) ListWorkflowRuns(_ context.Context, lc string) ([]v1alpha1.PolicyWorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]v1alpha1.PolicyWorkflowRun, 0, len(f.pwi))
	for ref, run := range f.pwi {
		if ref.LogicalCluster == lc {
			out = append(out, *run.DeepCopy())
		}
	}
	return out, nil
}

func (f *fakeRuntime) CreateWorkflowRun(_ context.Context, lc string, run *v1alpha1.PolicyWorkflowRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pwi[Ref{LogicalCluster: lc, Name: run.Name}] = run.DeepCopy()
	return nil
}

func (f *fakeRuntime) ReadEngine(_ context.Context, ref Ref) (*v1alpha1.PolicyEngine, error) {
	if v, ok := f.engines[ref]; ok {
		return v.DeepCopy(), nil
	}
	return nil, nil
}

func (f *fakeRuntime) WriteEngineStatus(_ context.Context, ref Ref, st v1alpha1.PolicyEngineStatus) error {
	if v, ok := f.engines[ref]; ok {
		v.Status = st
	}
	return nil
}

func (f *fakeRuntime) DeleteEngine(_ context.Context, ref Ref) error {
	delete(f.engines, ref)
	return nil
}

func (f *fakeRuntime) RemoveEngineFinalizer(context.Context, Ref) error { return nil }

func (f *fakeRuntime) ReadOpenBao(context.Context, Ref) (*v1alpha1.OpenBao, error) {
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupName, Resource: "openbaos"}, "openbao")
}

func (f *fakeRuntime) ListOpenBaos(_ context.Context, _, namespace string) ([]v1alpha1.OpenBao, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []v1alpha1.OpenBao
	for _, obj := range f.openBaos {
		if obj.Namespace == namespace {
			out = append(out, *obj.DeepCopy())
		}
	}
	return out, nil
}

func (f *fakeRuntime) WriteOpenBaoStatus(_ context.Context, ref Ref, st v1alpha1.OpenBaoStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openBaoStatus[ref] = st
	return nil
}

func (f *fakeRuntime) WriteOpenBaoFinalizer(_ context.Context, ref Ref, finalizers []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openBaoFinalizers[ref] = finalizers
	return nil
}

func (f *fakeRuntime) RemoveOpenBaoFinalizer(context.Context, Ref) error { return nil }

func (f *fakeRuntime) DeleteOpenBao(context.Context, Ref) error { return nil }

func (f *fakeRuntime) ReadWorkflowPod(_ context.Context, ref Ref) (*v1alpha1.PolicyWorkflowPod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.workflowPods[ref]; ok {
		return v.DeepCopy(), nil
	}
	return nil, nil
}

func (f *fakeRuntime) WriteWorkflowPodStatus(_ context.Context, ref Ref, st v1alpha1.PolicyWorkflowPodStatus) error {
	if v, ok := f.workflowPods[ref]; ok {
		v.Status = st
	}
	return nil
}

func (f *fakeRuntime) DeleteWorkflowPod(_ context.Context, ref Ref) error {
	delete(f.workflowPods, ref)
	return nil
}

func (f *fakeRuntime) Read(_ context.Context, ref Ref) (*v1alpha1.PolicyWorkflowRun, error) {
	if v, ok := f.pwi[ref]; ok {
		return v.DeepCopy(), nil
	}
	return nil, nil
}

func (f *fakeRuntime) WriteStatus(context.Context, Ref, v1alpha1.PolicyWorkflowRunStatus) error {
	return nil
}

func (f *fakeRuntime) Delete(context.Context, Ref) error { return nil }

func (f *fakeRuntime) RemoveFinalizer(context.Context, Ref) error { return nil }

func (f *fakeRuntime) ReadPod(_ context.Context, ref Ref) (*v1alpha1.DenoPod, error) {
	if v, ok := f.pods[ref]; ok {
		return v.DeepCopy(), nil
	}
	return nil, nil
}

func (f *fakeRuntime) CreatePod(_ context.Context, lc string, pod *v1alpha1.DenoPod) error {
	f.pods[Ref{LogicalCluster: lc, Name: pod.Name}] = pod.DeepCopy()
	return nil
}

func (f *fakeRuntime) WritePodStatus(_ context.Context, ref Ref, st v1alpha1.DenoPodStatus) error {
	if v, ok := f.pods[ref]; ok {
		v.Status = st
	}
	return nil
}

func (f *fakeRuntime) DeletePod(_ context.Context, ref Ref) error {
	delete(f.pods, ref)
	return nil
}

func (f *fakeRuntime) RemovePodFinalizer(context.Context, Ref) error { return nil }

func (f *fakeRuntime) ReadRun(_ context.Context, ref Ref) (*v1alpha1.DenoRun, error) {
	if v, ok := f.runs[ref]; ok {
		return v.DeepCopy(), nil
	}
	return nil, nil
}

func (f *fakeRuntime) CreateRun(_ context.Context, lc string, run *v1alpha1.DenoRun) error {
	f.runs[Ref{LogicalCluster: lc, Name: run.Name}] = run.DeepCopy()
	return nil
}

func (f *fakeRuntime) WriteRunStatus(_ context.Context, ref Ref, st v1alpha1.DenoRunStatus) error {
	if v, ok := f.runs[ref]; ok {
		v.Status = st
	}
	return nil
}

func (f *fakeRuntime) DeleteRun(_ context.Context, ref Ref) error {
	delete(f.runs, ref)
	return nil
}

func (f *fakeRuntime) RemoveRunFinalizer(context.Context, Ref) error { return nil }

func (f *fakeRuntime) ReadJob(_ context.Context, ref Ref) (*v1alpha1.DenoJob, error) {
	if v, ok := f.jobs[ref]; ok {
		return v.DeepCopy(), nil
	}
	return nil, nil
}

func (f *fakeRuntime) CreateJob(_ context.Context, lc string, job *v1alpha1.DenoJob) error {
	f.jobs[Ref{LogicalCluster: lc, Name: job.Name}] = job.DeepCopy()
	return nil
}

func (f *fakeRuntime) WriteJobStatus(_ context.Context, ref Ref, st v1alpha1.DenoJobStatus) error {
	if v, ok := f.jobs[ref]; ok {
		v.Status = st
	}
	return nil
}

func (f *fakeRuntime) DeleteJob(_ context.Context, ref Ref) error {
	delete(f.jobs, ref)
	return nil
}

func (f *fakeRuntime) ReadTrigger(_ context.Context, ref Ref) (*v1alpha1.RunTrigger, error) {
	if v, ok := f.triggers[ref]; ok {
		return v.DeepCopy(), nil
	}
	return nil, nil
}

func (f *fakeRuntime) WriteTriggerStatus(_ context.Context, ref Ref, st v1alpha1.RunTriggerStatus) error {
	if v, ok := f.triggers[ref]; ok {
		v.Status = st
	}
	return nil
}

type fakeMinter struct {
	calls int

	token string
}

func (m *fakeMinter) MintServiceAccountToken(context.Context, string, string, string, time.Duration) (string, error) {
	m.calls++
	return m.token, nil
}

func runtimeProvider(t *testing.T, rt *fakeRuntime, pods runner.PodRunner, minter TokenMinter) *Provider {
	t.Helper()
	p, err := New(Options{
		Registry:     rt,
		PolicyClient: newFakePolicyClient(false),
		Runtime:      rt,
		RestConfig:   &rest.Config{Host: "https://kcp"},
		Minter:       minter,
		PodRunner:    pods,
		WriteStatus:  true,
		Host:         "https://kcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReconcilePodMintsATokenAndReportsOutputs(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "pod-1"}
	rt.pods[ref] = &v1alpha1.DenoPod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Generation: 1},
		Spec: v1alpha1.DenoPodSpec{
			DenoPodTemplate: v1alpha1.DenoPodTemplate{
				Script:         `console.log("hi")`,
				ServiceAccount: &v1alpha1.ServiceAccountRef{Name: "deno"},
			},
			RestartPolicy: v1alpha1.RestartNever,
		},
	}
	minter := &fakeMinter{token: "tok"}
	mem := memoryrunner.NewPod(memoryrunner.PodOptions{
		Outcome:         runner.PodStatus{State: runner.StateSucceeded, Outputs: map[string]string{"allow": "true"}},
		PollsBeforeDone: 1,
	})
	p := runtimeProvider(t, rt, mem, minter)
	ctx := context.Background()

	if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if rt.pods[ref].Status.RunID == "" {
		t.Fatal("a run should have been started")
	}
	if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := rt.pods[ref].Status.Phase; got != v1alpha1.DenoPodSucceeded {
		t.Fatalf("phase = %s, want Succeeded", got)
	}
	if rt.pods[ref].Status.Outputs["allow"] != "true" {
		t.Fatalf("outputs = %v", rt.pods[ref].Status.Outputs)
	}

	if minter.calls != 2 {
		t.Fatalf("mint calls = %d, want 2", minter.calls)
	}
}

func TestLongRunningPodRestartsWithAlways(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "pod-1"}
	rt.pods[ref] = &v1alpha1.DenoPod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Generation: 1},
		Spec: v1alpha1.DenoPodSpec{
			DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"},
			RestartPolicy:   v1alpha1.RestartAlways,
		},
	}
	mem := memoryrunner.NewPod(memoryrunner.PodOptions{
		Outcome:         runner.PodStatus{State: runner.StateSucceeded},
		PollsBeforeDone: 1,
	})
	p := runtimeProvider(t, rt, mem, nil)
	ctx := context.Background()

	if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := rt.pods[ref].Status; got.Phase != v1alpha1.DenoPodRunning || got.Restarts != 1 || got.RunID != "" {
		t.Fatalf("status = %+v, want Running with one restart", got)
	}
}

func TestReconcileRunStartsAndSucceeds(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "run-1"}
	rt.runs[ref] = &v1alpha1.DenoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Generation: 1},
		Spec:       v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}},
	}
	mem := memoryrunner.NewPod(memoryrunner.PodOptions{
		Outcome:         runner.PodStatus{State: runner.StateSucceeded, Outputs: map[string]string{"allow": "true"}},
		PollsBeforeDone: 1,
	})
	p := runtimeProvider(t, rt, mem, nil)
	ctx := context.Background()

	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if rt.runs[ref].Status.RunID == "" {
		t.Fatal("the run should have started a deno process")
	}
	if _, _, err := p.process(ctx, workKey{kind: workRun, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := rt.runs[ref].Status.Phase; got != v1alpha1.DenoRunSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", got)
	}
	if rt.runs[ref].Status.Outputs["allow"] != "true" {
		t.Fatalf("run outputs = %v", rt.runs[ref].Status.Outputs)
	}
}

func TestReconcileJobCreatesARunThenSucceeds(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "job-1"}
	rt.jobs[ref] = &v1alpha1.DenoJob{
		ObjectMeta: metav1.ObjectMeta{Name: "job-1", Generation: 1, UID: "job-uid"},
		Spec:       v1alpha1.DenoJobSpec{Template: v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}},
	}
	p := runtimeProvider(t, rt, memoryrunner.NewPod(memoryrunner.PodOptions{}), nil)
	ctx := context.Background()

	if _, _, err := p.process(ctx, workKey{kind: workJob, ref: ref}); err != nil {
		t.Fatal(err)
	}
	job := rt.jobs[ref]
	if job.Status.RunName != "job-1-1" {
		t.Fatalf("runName = %q, want job-1-1", job.Status.RunName)
	}
	runRef := Ref{LogicalCluster: "root:demo", Name: "job-1-1"}
	run, ok := rt.runs[runRef]
	if !ok {
		t.Fatal("the job should have created its deno run")
	}
	if len(run.OwnerReferences) != 1 || run.OwnerReferences[0].Kind != "DenoJob" || run.OwnerReferences[0].UID != "job-uid" {
		t.Fatalf("run owner references = %+v, want a DenoJob owner", run.OwnerReferences)
	}

	rt.runs[runRef].Status = v1alpha1.DenoRunStatus{
		Phase:   v1alpha1.DenoRunSucceeded,
		Outputs: map[string]string{"allow": "true"},
	}
	if _, _, err := p.process(ctx, workKey{kind: workJob, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := rt.jobs[ref].Status.Phase; got != v1alpha1.DenoJobSucceeded {
		t.Fatalf("job phase = %s, want Succeeded", got)
	}
	if rt.jobs[ref].Status.Outputs["allow"] != "true" {
		t.Fatalf("job outputs = %v", rt.jobs[ref].Status.Outputs)
	}
}

func TestReconcileTriggerCreatesAJobOnMatch(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "watch"}
	rt.triggers[ref] = &v1alpha1.RunTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "watch", Generation: 1},
		Spec: v1alpha1.RunTriggerSpec{
			PolicyWorkflowPod: "open-policy-pod",
			Match:             map[string]string{"allow": "true"},
			JobTemplate:       v1alpha1.DenoJobSpec{Template: v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}},
		},
	}
	rt.workflowPods[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod"}] = &v1alpha1.PolicyWorkflowPod{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod"},
	}
	rt.pwi[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod-100"}] = &v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod-100", Labels: map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}},
		Status: v1alpha1.PolicyWorkflowRunStatus{
			Phase:   v1alpha1.PolicyWorkflowSucceeded,
			Outputs: map[string]string{"allow": "true"},
		},
	}
	p := runtimeProvider(t, rt, memoryrunner.NewPod(memoryrunner.PodOptions{}), nil)

	if _, _, err := p.process(context.Background(), workKey{kind: workTrigger, ref: ref}); err != nil {
		t.Fatal(err)
	}
	tr := rt.triggers[ref]
	if tr.Status.Phase != v1alpha1.RunTriggerTriggered {
		t.Fatalf("trigger phase = %s", tr.Status.Phase)
	}
	if tr.Status.LastRun != "open-policy-pod-100" {
		t.Fatalf("lastRun = %q, want open-policy-pod-100", tr.Status.LastRun)
	}
	if _, ok := rt.jobs[Ref{LogicalCluster: "root:demo", Name: "watch-open-policy-pod-100"}]; !ok {
		t.Fatalf("the trigger should have created the job watch-open-policy-pod-100: %v", rt.jobs)
	}
}

func TestReconcileTriggerPicksTheNewestTerminalRunOfThePod(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "watch"}
	rt.triggers[ref] = &v1alpha1.RunTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "watch", Generation: 1},
		Spec: v1alpha1.RunTriggerSpec{
			PolicyWorkflowPod: "open-policy-pod",
			JobTemplate:       v1alpha1.DenoJobSpec{Template: v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}},
		},
	}
	rt.workflowPods[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod"}] = &v1alpha1.PolicyWorkflowPod{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod"},
	}
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	older := metav1.NewTime(base)
	mid := metav1.NewTime(base.Add(time.Minute))
	newer := metav1.NewTime(base.Add(2 * time.Minute))
	later := metav1.NewTime(base.Add(3 * time.Minute))
	rt.pwi[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod-100"}] = &v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod-100", CreationTimestamp: older, Labels: map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}},
		Status:     v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded, Outputs: map[string]string{"allow": "true"}},
	}
	rt.pwi[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod-200"}] = &v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod-200", CreationTimestamp: mid, Labels: map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}},
		Status:     v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded, Outputs: map[string]string{"allow": "true"}},
	}
	rt.pwi[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod-300"}] = &v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod-300", CreationTimestamp: newer, Labels: map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}},
		Status:     v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowRunning},
	}
	rt.pwi[Ref{LogicalCluster: "root:demo", Name: "other-pod-1"}] = &v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "other-pod-1", CreationTimestamp: later, Labels: map[string]string{v1alpha1.PolicyWorkflowPodLabel: "other-pod"}},
		Status:     v1alpha1.PolicyWorkflowRunStatus{Phase: v1alpha1.PolicyWorkflowSucceeded, Outputs: map[string]string{"allow": "true"}},
	}
	p := runtimeProvider(t, rt, memoryrunner.NewPod(memoryrunner.PodOptions{}), nil)

	if _, _, err := p.process(context.Background(), workKey{kind: workTrigger, ref: ref}); err != nil {
		t.Fatal(err)
	}
	tr := rt.triggers[ref]
	if tr.Status.LastRun != "open-policy-pod-200" {
		t.Fatalf("lastRun = %q, want the newest terminal run open-policy-pod-200", tr.Status.LastRun)
	}
	if _, ok := rt.jobs[Ref{LogicalCluster: "root:demo", Name: "watch-open-policy-pod-200"}]; !ok {
		t.Fatalf("the trigger should have created watch-open-policy-pod-200: %v", rt.jobs)
	}
}

func TestReconcileTriggerSkipsACancelledRun(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:demo", Name: "watch"}
	rt.triggers[ref] = &v1alpha1.RunTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "watch", Generation: 1},
		Spec: v1alpha1.RunTriggerSpec{
			PolicyWorkflowPod: "open-policy-pod",
			Match:             map[string]string{"allow": "true"},
			JobTemplate:       v1alpha1.DenoJobSpec{Template: v1alpha1.DenoRunSpec{DenoPodTemplate: v1alpha1.DenoPodTemplate{Script: "x"}}},
		},
	}
	rt.workflowPods[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod"}] = &v1alpha1.PolicyWorkflowPod{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod"},
	}
	rt.pwi[Ref{LogicalCluster: "root:demo", Name: "open-policy-pod-100"}] = &v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "open-policy-pod-100", Labels: map[string]string{v1alpha1.PolicyWorkflowPodLabel: "open-policy-pod"}},
		Status: v1alpha1.PolicyWorkflowRunStatus{
			Phase:   v1alpha1.PolicyWorkflowCancelled,
			Outputs: map[string]string{"allow": "true"},
		},
	}
	p := runtimeProvider(t, rt, memoryrunner.NewPod(memoryrunner.PodOptions{}), nil)

	if _, _, err := p.process(context.Background(), workKey{kind: workTrigger, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if rt.triggers[ref].Status.Phase != v1alpha1.RunTriggerSkipped {
		t.Fatalf("trigger phase = %s, want Skipped", rt.triggers[ref].Status.Phase)
	}
	if _, ok := rt.jobs[Ref{LogicalCluster: "root:demo", Name: "watch-open-policy-pod-100"}]; ok {
		t.Fatal("a cancelled run must not create a deno job")
	}
}
