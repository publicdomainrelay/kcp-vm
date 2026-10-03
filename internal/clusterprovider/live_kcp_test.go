//go:build linux

package clusterprovider

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
)

func TestLiveClusterIsReadAndNotDisturbed(t *testing.T) {
	livegate.Short(t, "this test reads a real kcp cluster and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	kubeconfig := os.Getenv("CLUSTERPROVIDER_LIVE_KUBECONFIG")
	if kubeconfig == "" {
		livegate.Require(t, "set CLUSTERPROVIDER_LIVE_KUBECONFIG to a kcp admin kubeconfig")
	}
	liveHost := os.Getenv("CLUSTERPROVIDER_LIVE_HOST")
	if liveHost == "" {
		liveHost = "https://127.0.0.1:6443"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("reading %s: %v", kubeconfig, err)
	}
	cfg.TLSClientConfig.Insecure = true
	cfg.TLSClientConfig.CAData = nil
	cfg.TLSClientConfig.CAFile = ""

	reg, err := NewRegistry(RegistryOptions{Host: liveHost, RestConfig: cfg})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	refs, err := reg.Refs(ctx)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	for _, r := range refs {
		t.Logf("ref account=%s home=%s name=%s did=%s mount=%s", r.Account, r.Home, r.Name, r.DID, r.MountPath())
	}
	if len(refs) == 0 {
		t.Fatal("no Cluster CRs found; the walk (root -> accounts -> homes -> clusters) read nothing")
	}

	observe := mustProvider(t, reg, nil, cfg)
	for _, r := range refs {
		pass, err := observe.Reconcile(ctx, r)
		if err != nil {
			t.Fatalf("observe-only reconcile of %s: %v", r.Name, err)
		}
		if !pass.ObserveOnly {
			t.Fatalf("%s: a provider with no Host was not observe-only", r.Name)
		}
		t.Logf("OBSERVE %s running=%v phase=%q url=%q instance=%q mountStatus=%d reachable=%v alert=%q",
			r.Name, pass.Cluster.Spec.Running, pass.Result.Phase, pass.Result.URL,
			pass.Result.Instance, pass.Observed.PathStatus, pass.Observed.Reachable, pass.Alert())
	}

	for _, r := range refs {
		h := &fakeHost{live: false}
		fc := &recordingClusters{Clusters: reg}
		withHost := mustProvider(t, fc, h, cfg)
		pass, err := withHost.Reconcile(ctx, r)
		if err != nil {
			t.Fatalf("reconcile of %s with a lifecycle: %v", r.Name, err)
		}
		if pass.Cluster.Status.URL == "" || !pass.Observed.Reachable {
			t.Logf("%s: url=%q reachable=%v -- nothing is serving it, so the pass is a normal decision "+
				"(phase=%q foreign=%v)", r.Name, pass.Cluster.Status.URL, pass.Observed.Reachable, pass.Result.Phase, pass.Foreign)
			continue
		}
		if !pass.Foreign {
			t.Fatalf("%s: an apiserver answers at %s and this process holds no group, but the pass was NOT "+
				"foreign -- it would have spawned over a live cluster", r.Name, pass.Cluster.Status.URL)
		}
		if n := len(h.spawned()); n != 0 {
			t.Fatalf("%s: spawned %d group(s) over a serving apiserver", r.Name, n)
		}
		if fc.wrote() != 0 {
			t.Fatalf("%s: a foreign pass wrote %d status(es)", r.Name, fc.wrote())
		}
		t.Logf("FOREIGN %s left alone: %s", r.Name, pass.Alert())
	}
}

type recordingClusters struct {
	Clusters
	mu     sync.Mutex
	writes []v1alpha1.ClusterStatus
}

func (c *recordingClusters) WriteStatus(_ context.Context, _ Ref, st v1alpha1.ClusterStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, st)
	return nil
}

func (c *recordingClusters) wrote() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}

func mustProvider(t *testing.T, clusters Clusters, host materialiser.Host, cfg *rest.Config) *Provider {
	t.Helper()
	dh := host
	if dh == nil {
		dh = &fakeHost{}
	}
	p, err := New(Options{
		Registry:    clusters,
		Decider:     decider(t, dh),
		Host:        host,
		WriteStatus: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}
