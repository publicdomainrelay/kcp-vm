//go:build linux

package providermain

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portscan"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func TestARestartAdoptsTheStoreBeforeItDerivesTheSpawnArgv(t *testing.T) {
	const cluster = "liveweb"
	ctx := context.Background()

	alloc := testAllocator(t)
	for _, p := range []portallocator.Purpose{
		portallocator.PurposePostgres,
		portallocator.PurposeKine,
		portallocator.PurposeK3sSecure,
		portallocator.PurposeK3sControllerManager,
		portallocator.PurposeK3sScheduler,
		portallocator.PurposeK3sCloudControllerManager,
	} {
		if _, err := alloc.Allocate(portallocator.Request{Cluster: cluster, Purpose: p}); err != nil {
			t.Fatalf("leasing %s for the restart: %v", p, err)
		}
	}

	adopted := map[string]bool{}
	storeEndpoint := func(name string) (string, error) {
		if !adopted[name] {
			return "", errors.New(
				"clusterprovider: no postgres handle for " + name + " in this process, so there " +
					"is no DSN to give k3s")
		}
		return "postgres://postgres:adopted@127.0.0.1:25432/k3s?sslmode=disable", nil
	}

	stackCfg := clusterprovider.StackConfig{
		Stack:            clusterprovider.Stack{procgroup.ComponentKine, procgroup.ComponentK3s},
		Admin:            func(context.Context, string, string) (kubernetes.Interface, error) { return nil, nil },
		StateDir:         t.TempDir(),
		StoreEndpoint:    storeEndpoint,
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      portallocator.LoopbackAdvertiseAddress,
		AuditPolicyPath:  filepath.Join(t.TempDir(), "audit-policy.yaml"),
	}

	decider, err := materialiser.New(materialiser.Options{
		Host:        materialiser.NewLocalHost(procgroup.Options{}),
		Ports:       alloc,
		Networks:    testNetworks(t),
		Steps:       clusterprovider.StepsFor(stackCfg),
		Identity:    func(context.Context, materialiser.Observed) error { return nil },
		NewInstance: instanceMinter(),
	})
	if err != nil {
		t.Fatalf("materialiser.New: %v", err)
	}

	registry := &fakeClusters{items: []v1alpha1.Cluster{{
		ObjectMeta: metav1.ObjectMeta{Name: cluster},
		Spec:       v1alpha1.ClusterSpec{Running: false},
	}}}

	_, err = Startup(ctx, StartupOptions{
		Allocator: alloc,
		Registry:  registry,
		StateDir:  stackCfg.StateDir,
		Stack:     stackCfg,
		Host:      materialiser.NewLocalHost(procgroup.Options{}),
		Decider:   decider,
		EnsureStore: func(_ context.Context, name string) error {
			adopted[name] = true
			return nil
		},
		Scan: func([]portallocator.Lease, []portscan.Stack) (portscan.Survivors, []portscan.Drop, error) {
			return portscan.Survivors{}, nil, nil
		},
		Log: testLogger(),
	})
	if err != nil {
		t.Fatalf("a restart over a lease file with no store handle in this process was refused: %v\n\n"+
			"The store is a library child and not a Step, so nothing reaps it: the restart reap "+
			"derives the argv of the group it may spawn, that argv carries the DSN, and a pass "+
			"that derives it before adopting the store refuses -- docker restart becomes a crash "+
			"loop on the second run of every deployment", err)
	}
	if !adopted[cluster] {
		t.Errorf("the store of %s was never adopted before the argv was derived", cluster)
	}
}
