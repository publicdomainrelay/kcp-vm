package provider

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

var admissionBase = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func admissionRun(name, pod string, phase v1alpha1.PolicyWorkflowPhase, at time.Time) v1alpha1.PolicyWorkflowRun {
	return v1alpha1.PolicyWorkflowRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(at),
			Labels:            map[string]string{v1alpha1.PolicyWorkflowPodLabel: pod},
		},
		Spec:   v1alpha1.PolicyWorkflowRunSpec{PolicyWorkflowPod: pod},
		Status: v1alpha1.PolicyWorkflowRunStatus{Phase: phase},
	}
}

func admissionPod(name string, policy v1alpha1.ConcurrencyPolicy, max *int32, endpoint string) v1alpha1.PolicyWorkflowPod {
	return v1alpha1.PolicyWorkflowPod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.PolicyWorkflowPodSpec{
			Workflow:          runtime.RawExtension{Raw: []byte(`{"name":"from-pod"}`)},
			Inputs:            map[string]string{"self-did": "did:plc:pod"},
			PolicyEngine:      "policy-engine",
			ConcurrencyPolicy: policy,
			MaxConcurrent:     max,
		},
		Status: v1alpha1.PolicyWorkflowPodStatus{
			Phase:    v1alpha1.PolicyWorkflowPodRunning,
			Endpoint: endpoint,
		},
	}
}

func admissionFor(t *testing.T, runs []v1alpha1.PolicyWorkflowRun, pods []v1alpha1.PolicyWorkflowPod, pod, run string) runAdmission {
	t.Helper()
	rt := newFakeRuntime()
	for i := range pods {
		obj := pods[i]
		rt.workflowPods[Ref{LogicalCluster: "root:demo", Name: obj.Name}] = &obj
	}
	refs := make(map[string]Ref, len(runs))
	for i := range runs {
		obj := runs[i]
		key := Ref{LogicalCluster: "root:demo", Namespace: obj.Namespace, Name: obj.Name}
		rt.pwi[key] = &obj
		refs[obj.Name] = key
	}
	target, ok := refs[run]
	if !ok {
		t.Fatalf("no run %s", run)
	}
	p := runtimeProvider(t, rt, nil, nil)
	adm, err := p.admit(context.Background(), target, rt.pwi[target])
	if err != nil {
		t.Fatalf("admit %s: %v", run, err)
	}
	return adm
}

func TestForbidAdmitsTheOldestPendingAndQueuesTheRest(t *testing.T) {
	pod := admissionPod("pod", v1alpha1.ConcurrencyForbid, nil, "http://127.0.0.1:1")
	running := admissionRun("a", "pod", v1alpha1.PolicyWorkflowRunning, admissionBase)
	pending1 := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(time.Second))
	pending2 := admissionRun("c", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(2*time.Second))

	runs := []v1alpha1.PolicyWorkflowRun{running, pending1, pending2}
	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "a"); a.gated {
		t.Fatal("a running run must not be gated")
	}
	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "b"); a.allowed {
		t.Fatal("one slot is taken, so b must queue")
	}
	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "c"); a.allowed {
		t.Fatal("c must queue behind b")
	}

	none := []v1alpha1.PolicyWorkflowRun{pending1, pending2}
	if a := admissionFor(t, none, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "b"); !a.allowed {
		t.Fatal("with no active run, the oldest pending run must start")
	}
	if a := admissionFor(t, none, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "c"); a.allowed {
		t.Fatal("the newer pending run must queue behind the oldest")
	}
}

func TestAllowFillsEveryFreeSlotOnce(t *testing.T) {
	cap := int32(2)
	pod := admissionPod("pod", v1alpha1.ConcurrencyAllow, &cap, "http://127.0.0.1:1")
	running := admissionRun("a", "pod", v1alpha1.PolicyWorkflowRunning, admissionBase)
	b := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(time.Second))
	c := admissionRun("c", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(2*time.Second))
	runs := []v1alpha1.PolicyWorkflowRun{running, b, c}

	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "b"); !a.allowed {
		t.Fatal("one free slot: b must start")
	}
	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "c"); a.allowed {
		t.Fatal("no free slot left after b: c must queue")
	}
}

func TestUnlimitedAllowAdmitsEverything(t *testing.T) {
	pod := admissionPod("pod", v1alpha1.ConcurrencyAllow, nil, "http://127.0.0.1:1")
	b := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase)
	c := admissionRun("c", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(time.Second))
	runs := []v1alpha1.PolicyWorkflowRun{b, c}

	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "b"); !a.allowed {
		t.Fatal("unset maxConcurrent must be unlimited")
	}
	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "c"); !a.allowed {
		t.Fatal("unset maxConcurrent must be unlimited")
	}
}

func TestAdmissionResolvesEndpointWorkflowInputsAndTTLFromThePod(t *testing.T) {
	ttl := int64(45)
	pod := admissionPod("pod", v1alpha1.ConcurrencyForbid, nil, "http://127.0.0.1:7777")
	pod.Spec.RunTTLSecondsAfterFinished = &ttl
	b := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase)

	a := admissionFor(t, []v1alpha1.PolicyWorkflowRun{b}, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "b")
	if a.endpoint != "http://127.0.0.1:7777" {
		t.Fatalf("endpoint = %q", a.endpoint)
	}
	if string(a.workflow) != `{"name":"from-pod"}` {
		t.Fatalf("workflow = %s", a.workflow)
	}
	if a.inputs["self-did"] != "did:plc:pod" {
		t.Fatalf("inputs = %v", a.inputs)
	}
	if a.ttl == nil || *a.ttl != 45 {
		t.Fatalf("ttl = %v, want 45", a.ttl)
	}
}

func TestAdmissionBlocksWhenThePodOrEngineIsMissing(t *testing.T) {
	orphan := admissionRun("b", "gone", v1alpha1.PolicyWorkflowPending, admissionBase)
	a := admissionFor(t, []v1alpha1.PolicyWorkflowRun{orphan}, nil, "gone", "b")
	if a.allowed || a.reason != "PolicyWorkflowPodMissing" {
		t.Fatalf("orphan admission = %+v", a)
	}

	pod := admissionPod("pod", v1alpha1.ConcurrencyForbid, nil, "")
	b := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase)
	a = admissionFor(t, []v1alpha1.PolicyWorkflowRun{b}, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "b")
	if a.allowed || a.reason != "EngineNotReady" {
		t.Fatalf("not-ready admission = %+v", a)
	}
}

func TestReplaceAdmitsOnlyTheNewestAndPreemptsTheRest(t *testing.T) {
	pod := admissionPod("pod", v1alpha1.ConcurrencyReplace, nil, "http://127.0.0.1:1")
	running := admissionRun("a", "pod", v1alpha1.PolicyWorkflowRunning, admissionBase)
	b := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(time.Second))
	c := admissionRun("c", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(2*time.Second))
	runs := []v1alpha1.PolicyWorkflowRun{running, b, c}

	if a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "b"); a.allowed || a.reason != "Superseded" {
		t.Fatalf("b admission = %+v, want superseded", a)
	}
	a := admissionFor(t, runs, []v1alpha1.PolicyWorkflowPod{pod}, "pod", "c")
	if !a.allowed {
		t.Fatalf("the newest replace run must start: %+v", a)
	}
	if len(a.preempt) != 2 {
		t.Fatalf("preempt = %v, want the running run and the older pending run", a.preempt)
	}
}

func TestQueuedRunDoesNotSubmitUntilAdmitted(t *testing.T) {
	store := newRun(v1alpha1.PolicyWorkflowRunSpec{PolicyWorkflowPod: "pod"})
	p := newProvider(t, store, newFakePolicyClient(false))
	ctx := context.Background()

	_, err := p.reconcileWorkflowRun(ctx, Ref{LogicalCluster: "root:demo", Name: "demo"}, store.inst, runAdmission{
		gated:    true,
		allowed:  false,
		reason:   "AtCapacity",
		message:  "waiting for a free slot",
		endpoint: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowPending || store.inst.Status.RunID != "" {
		t.Fatalf("queued run status = %+v, want Pending with no runID", store.inst.Status)
	}

	_, err = p.reconcileWorkflowRun(ctx, Ref{LogicalCluster: "root:demo", Name: "demo"}, store.inst, runAdmission{
		gated:    true,
		allowed:  true,
		endpoint: "http://127.0.0.1:1",
		workflow: []byte(`{"name":"from-pod"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.inst.Status.Phase != v1alpha1.PolicyWorkflowRunning || store.inst.Status.RunID == "" {
		t.Fatalf("admitted run status = %+v, want Running with a runID", store.inst.Status)
	}
}

type versionTolerantRuntime struct {
	*fakeRuntime
}

func (r versionTolerantRuntime) Read(ctx context.Context, ref Ref) (*v1alpha1.PolicyWorkflowRun, error) {
	return r.fakeRuntime.Read(ctx, ref.WithResourceVersion(""))
}

func admissionProvider(t *testing.T, runs []v1alpha1.PolicyWorkflowRun, pods []v1alpha1.PolicyWorkflowPod) (*Provider, *fakeRuntime, map[string]Ref) {
	t.Helper()
	rt := newFakeRuntime()
	for i := range pods {
		obj := pods[i]
		rt.workflowPods[Ref{LogicalCluster: "root:demo", Name: obj.Name}] = &obj
	}
	refs := make(map[string]Ref, len(runs))
	for i := range runs {
		obj := runs[i]
		key := Ref{LogicalCluster: "root:demo", Namespace: obj.Namespace, Name: obj.Name}
		rt.pwi[key] = &obj
		refs[obj.Name] = key
	}
	p := runtimeProvider(t, rt, nil, nil)
	p.opts.Registry = versionTolerantRuntime{rt}
	return p, rt, refs
}

func TestReplaceDoesNotPreemptTheRunItAdmits(t *testing.T) {
	pod := admissionPod("pod", v1alpha1.ConcurrencyReplace, nil, "http://127.0.0.1:1")
	running := admissionRun("a", "pod", v1alpha1.PolicyWorkflowRunning, admissionBase)
	b := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(time.Second))
	c := admissionRun("c", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(2*time.Second))
	p, rt, refs := admissionProvider(t, []v1alpha1.PolicyWorkflowRun{running, b, c}, []v1alpha1.PolicyWorkflowPod{pod})

	adm, err := p.admit(context.Background(), refs["c"].WithResourceVersion("7"), rt.pwi[refs["c"]])
	if err != nil {
		t.Fatal(err)
	}
	if !adm.allowed {
		t.Fatalf("the newest run must start: %+v", adm)
	}
	if len(adm.preempt) != 2 {
		t.Fatalf("preempt = %v, want the running run and the older pending run", adm.preempt)
	}
	for _, pre := range adm.preempt {
		if pre.Name == "c" {
			t.Fatalf("the admitted run preempts itself: %v", adm.preempt)
		}
	}
}

func TestOneRunningRunCountsOnce(t *testing.T) {
	pod := admissionPod("pod", v1alpha1.ConcurrencyForbid, nil, "http://127.0.0.1:1")
	running := admissionRun("a", "pod", v1alpha1.PolicyWorkflowRunning, admissionBase)
	p, rt, refs := admissionProvider(t, []v1alpha1.PolicyWorkflowRun{running}, []v1alpha1.PolicyWorkflowPod{pod})

	adm, err := p.admitRun(context.Background(), refs["a"].WithResourceVersion("7"), rt.pwi[refs["a"]])
	if err != nil {
		t.Fatal(err)
	}
	if adm.Active != 1 {
		t.Fatalf("active = %d, want the one running run counted once", adm.Active)
	}
}

func TestALeaseIsRetiredOnceTheRunIsObservedRunning(t *testing.T) {
	pod := admissionPod("pod", v1alpha1.ConcurrencyReplace, nil, "http://127.0.0.1:1")
	running := admissionRun("a", "pod", v1alpha1.PolicyWorkflowRunning, admissionBase)
	b := admissionRun("b", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(time.Second))
	c := admissionRun("c", "pod", v1alpha1.PolicyWorkflowPending, admissionBase.Add(2*time.Second))
	p, rt, refs := admissionProvider(t, []v1alpha1.PolicyWorkflowRun{running, b, c}, []v1alpha1.PolicyWorkflowPod{pod})

	if _, err := p.admit(context.Background(), refs["c"].WithResourceVersion("7"), rt.pwi[refs["c"]]); err != nil {
		t.Fatal(err)
	}
	rt.pwi[refs["c"]].Status.Phase = v1alpha1.PolicyWorkflowRunning
	observed := map[Ref]string{}
	for _, key := range []Ref{refs["a"], refs["b"], refs["c"]} {
		observed[key] = string(rt.pwi[key].Status.Phase)
	}
	parent := Ref{LogicalCluster: "root:demo", Name: "pod"}
	if got := p.admissions.Leases().Count(parent, observed, p.opts.Now(), workflowRunLifecycle()); got != 0 {
		t.Fatalf("leases held = %d, want the lease retired once the run is observed running", got)
	}
}
