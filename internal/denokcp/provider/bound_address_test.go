package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/assets"
)

type boundRunner struct {
	mu sync.Mutex

	port int32

	starts int

	probes [][]string
}

func (b *boundRunner) Start(context.Context, runner.PodRequest) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	return "run-1", nil
}

func (b *boundRunner) Observe(context.Context, string) (runner.PodStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return runner.PodStatus{State: runner.StateRunning, BoundPort: b.port}, nil
}

func (b *boundRunner) Stop(context.Context, string) error { return nil }

func (b *boundRunner) Probe(_ context.Context, _ string, command []string, _ time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probes = append(b.probes, command)
	return true, nil
}

func boundProvider(t *testing.T, rt *fakeRuntime, pods runner.PodRunner) *Provider {
	t.Helper()
	p, err := New(Options{
		Registry:     rt,
		PolicyClient: newFakePolicyClient(false),
		Runtime:      rt,
		RestConfig:   &rest.Config{Host: "https://kcp"},
		PodRunner:    pods,
		WriteStatus:  true,
		Host:         "https://kcp",
		RunsDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func portZeroPod(name string, probe bool) *v1alpha1.DenoPod {
	pod := &v1alpha1.DenoPod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: 1},
		Spec: v1alpha1.DenoPodSpec{
			DenoPodTemplate: v1alpha1.DenoPodTemplate{
				Script: "x",
				Env:    map[string]string{"SERVICE_ARGS": `["--port","0","--hostname","127.0.0.1"]`},
			},
			RestartPolicy: v1alpha1.RestartAlways,
		},
	}
	if probe {
		pod.Spec.ReadinessProbe = &v1alpha1.ExecProbe{Command: []string{"kcpdns", "pds.default.alice.svc.kcp.local", "/xrpc/_health"}}
	}
	return pod
}

func TestAPodThatBindsPortZeroReportsItsBoundAddress(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "pds"}
	rt.pods[ref] = portZeroPod("pds", false)
	pods := &boundRunner{}
	p := boundProvider(t, rt, pods)
	ctx := context.Background()

	if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := rt.pods[ref].Status.Address; got != "" {
		t.Fatalf("address = %q before the process reported any port", got)
	}
	pods.port = 43210
	if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
		t.Fatal(err)
	}
	if got := rt.pods[ref].Status.Address; got != "127.0.0.1:43210" {
		t.Fatalf("address = %q, want the port the process bound on the host its manifest names", got)
	}
}

func TestTheTableFileCarriesTheBoundAddress(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "pds"}
	rt.pods[ref] = portZeroPod("pds", false)
	pods := &boundRunner{port: 43211}
	p := boundProvider(t, rt, pods)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(assets.DNSTablePath(p.opts.RunsDir))
	if err != nil {
		t.Fatalf("no table file: %v", err)
	}
	var table map[string]string
	if err := json.Unmarshal(body, &table); err != nil {
		t.Fatal(err)
	}
	name := p.serviceResolver().Name(ctx, ref.Name, ref.Namespace, ref.LogicalCluster)
	if table[name] != "127.0.0.1:43211" {
		t.Fatalf("table file = %v, want %s at 127.0.0.1:43211", table, name)
	}
	env := p.podEnv(ctx, ref, &rt.pods[ref].Spec.DenoPodTemplate)
	if env[assets.TableFileEnv] != assets.DNSTablePath(p.opts.RunsDir) {
		t.Fatalf("%s = %q, a workload must be told where the table it re-reads lives", assets.TableFileEnv, env[assets.TableFileEnv])
	}
}

func TestTheReadinessProbeMayReadTheTableFile(t *testing.T) {
	rt := newFakeRuntime()
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "pds"}
	rt.pods[ref] = portZeroPod("pds", true)
	pods := &boundRunner{port: 43212}
	p := boundProvider(t, rt, pods)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, err := p.process(ctx, workKey{kind: workPod, ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	if len(pods.probes) == 0 {
		t.Fatal("the readiness probe never ran")
	}
	joined := strings.Join(pods.probes[len(pods.probes)-1], " ")
	want := "--allow-read=" + filepath.Join(p.opts.RunsDir, assets.DNSDirName, assets.TableName)
	if !strings.Contains(joined, want) {
		t.Fatalf("probe = %s, want %s so a port 0 pod's own name resolves", joined, want)
	}
	if !strings.Contains(joined, assets.TableFileEnv) {
		t.Fatalf("probe = %s, the probe must be allowed to read %s", joined, assets.TableFileEnv)
	}
}
