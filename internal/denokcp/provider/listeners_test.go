package provider

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/listeners"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/assets"
)

type listenerRunner struct {
	mu sync.Mutex

	reported []listeners.Listener

	starts int
}

func (l *listenerRunner) Start(context.Context, runner.PodRequest) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.starts++
	return "run-1", nil
}

func (l *listenerRunner) Observe(context.Context, string) (runner.PodStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return runner.PodStatus{State: runner.StateRunning, Listeners: l.reported}, nil
}

func (l *listenerRunner) Stop(context.Context, string) error { return nil }

func (l *listenerRunner) Probe(context.Context, string, []string, time.Duration) (bool, error) {
	return true, nil
}

func processTimes(t *testing.T, p *Provider, ref Ref, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, _, err := p.process(context.Background(), workKey{kind: workPod, ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestADenoPodWithTwoTcpListenersAndAUnixListenerReportsAllThree(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "pds"}
	rt.pods[ref] = portZeroPod("pds", false)
	pods := &listenerRunner{}
	p := boundProvider(t, rt, pods)
	processTimes(t, p, ref, 2)
	if got := rt.pods[ref].Status.Listeners; len(got) != 0 {
		t.Fatalf("listeners = %+v before the process reported any", got)
	}

	pods.mu.Lock()
	pods.reported = []listeners.Listener{
		{Name: "http", Kind: listeners.KindTCP, Address: "127.0.0.1:41401"},
		{Name: "admin", Kind: listeners.KindTCP, Address: "127.0.0.1:41402"},
		{Name: "control", Kind: listeners.KindUnix, Address: "/runs/pds/control.sock"},
	}
	pods.mu.Unlock()
	processTimes(t, p, ref, 1)

	status := rt.pods[ref].Status
	want := []v1alpha1.DenoPodListener{
		{Name: "http", Kind: "tcp", Address: "127.0.0.1:41401"},
		{Name: "admin", Kind: "tcp", Address: "127.0.0.1:41402"},
		{Name: "control", Kind: "unix", Address: "/runs/pds/control.sock"},
	}
	if !reflect.DeepEqual(status.Listeners, want) {
		t.Fatalf("status.listeners = %+v, want %+v", status.Listeners, want)
	}
	if status.Address != "127.0.0.1:41401" {
		t.Fatalf("status.address = %q, want the first tcp listener", status.Address)
	}

	body, err := os.ReadFile(assets.DNSTablePath(p.opts.RunsDir))
	if err != nil {
		t.Fatalf("no table file: %v", err)
	}
	var table map[string]string
	if err := json.Unmarshal(body, &table); err != nil {
		t.Fatal(err)
	}
	name := p.serviceResolver().Name(context.Background(), ref.Name, ref.Namespace, ref.LogicalCluster)
	for key, value := range map[string]string{
		name:                                 "127.0.0.1:41401",
		listeners.TableName("http", name):    "127.0.0.1:41401",
		listeners.TableName("admin", name):   "127.0.0.1:41402",
		listeners.TableName("control", name): "unix:/runs/pds/control.sock",
	} {
		if table[key] != value {
			t.Fatalf("table[%s] = %q, want %q (table %v)", key, table[key], value, table)
		}
	}

	pods.mu.Lock()
	pods.reported = pods.reported[:1]
	pods.mu.Unlock()
	processTimes(t, p, ref, 1)
	if got := rt.pods[ref].Status.Listeners; len(got) != 1 {
		t.Fatalf("listeners = %+v, a listener the process closed leaves status", got)
	}
}

func fixedPortPod(name string, annotations map[string]string) *v1alpha1.DenoPod {
	pod := portZeroPod(name, false)
	pod.Annotations = annotations
	pod.Spec.Env = map[string]string{"SERVICE_ARGS": `["--port","2583","--hostname","127.0.0.1"]`}
	return pod
}

func TestAFixedPortBehindTheEdgeIsRefused(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "pds"}
	rt.pods[ref] = fixedPortPod("pds", nil)
	pods := &listenerRunner{}
	p := boundProvider(t, rt, pods)
	processTimes(t, p, ref, 2)
	if pods.starts != 0 {
		t.Fatalf("a DenoPod naming a fixed port behind the edge was started %d times", pods.starts)
	}
	status := rt.pods[ref].Status
	if status.Phase != v1alpha1.DenoPodFailed {
		t.Fatalf("phase = %s, want Failed", status.Phase)
	}
	refused := false
	for _, c := range status.Conditions {
		if c.Reason == v1alpha1.ReasonFixedPortBehindTheEdge {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("conditions = %+v, want reason %s", status.Conditions, v1alpha1.ReasonFixedPortBehindTheEdge)
	}
}

func TestAnEdgeDenoPodKeepsItsFixedPort(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:gateway", Namespace: "default", Name: "door"}
	rt.pods[ref] = fixedPortPod("door", map[string]string{v1alpha1.AnnotationEdge: "the door behind caddy"})
	pods := &listenerRunner{}
	p := boundProvider(t, rt, pods)
	processTimes(t, p, ref, 1)
	if pods.starts != 1 || rt.pods[ref].Status.Phase != v1alpha1.DenoPodRunning {
		t.Fatalf("starts=%d phase=%s, an edge pod keeps its known address", pods.starts, rt.pods[ref].Status.Phase)
	}
}
