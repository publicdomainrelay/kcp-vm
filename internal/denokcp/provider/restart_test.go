package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

type deadRunRunner struct {
	mu sync.Mutex

	live map[string]bool

	starts []string

	stops []string
}

func (d *deadRunRunner) Start(context.Context, runner.PodRequest) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := fmt.Sprintf("run-new-%d", len(d.starts)+1)
	d.starts = append(d.starts, id)
	d.live[id] = true
	return id, nil
}

func (d *deadRunRunner) Observe(_ context.Context, id string) (runner.PodStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live[id] {
		return runner.PodStatus{State: runner.StateRunning}, nil
	}
	return runner.PodStatus{State: runner.StateFailed, Message: "the deno process did not report an exit code"}, nil
}

func (d *deadRunRunner) Stop(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stops = append(d.stops, id)
	delete(d.live, id)
	return nil
}

func (d *deadRunRunner) Probe(context.Context, string, []string, time.Duration) (bool, error) {
	return true, nil
}

type refusingStatusRuntime struct {
	*fakeRuntime
}

func (refusingStatusRuntime) WritePodStatus(context.Context, Ref, v1alpha1.DenoPodStatus) error {
	return errors.New("the object has been modified; please apply your changes to the latest version")
}

func recordedPod(name, runID string) *v1alpha1.DenoPod {
	pod := portZeroPod(name, false)
	pod.Status.Phase = v1alpha1.DenoPodRunning
	pod.Status.RunID = runID
	return pod
}

func TestAPodWhoseRecordedRunDiedWithTheOldCoreStartsAgainExactlyOnce(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "svc"}
	rt.pods[ref] = recordedPod("svc", "pod-of-the-old-core")
	pods := &deadRunRunner{live: map[string]bool{}}
	p := boundProvider(t, rt, pods)
	for range 6 {
		if _, _, err := p.process(context.Background(), workKey{kind: workPod, ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	got := rt.pods[ref].Status
	if len(pods.starts) != 1 || got.RunID != pods.starts[0] || got.Restarts != 1 || got.Phase != v1alpha1.DenoPodRunning {
		t.Fatalf("starts %v, status runID %q restarts %d phase %s: want one start recorded as the pod's run", pods.starts, got.RunID, got.Restarts, got.Phase)
	}
}

func TestARunStartedForAStatusThatCannotBeWrittenIsStopped(t *testing.T) {
	base := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "svc"}
	base.pods[ref] = portZeroPod("svc", false)
	rt := refusingStatusRuntime{base}
	pods := &deadRunRunner{live: map[string]bool{}}
	p, err := New(Options{Registry: rt, PolicyClient: newFakePolicyClient(false), Runtime: rt,
		RestConfig: boundProvider(t, base, pods).opts.RestConfig, PodRunner: pods, WriteStatus: true, Host: "https://kcp", RunsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.process(context.Background(), workKey{kind: workPod, ref: ref}); err == nil {
		t.Fatal("a refused status write must fail the pass")
	}
	if len(pods.starts) != 1 || len(pods.stops) != 1 || pods.stops[0] != pods.starts[0] {
		t.Fatalf("starts %v stops %v: a run no status records is a second process on the next pass", pods.starts, pods.stops)
	}
}
