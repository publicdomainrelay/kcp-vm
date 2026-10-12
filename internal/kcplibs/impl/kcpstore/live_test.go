//go:build linux

package kcpstore

import (
	"context"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/statuspatch"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/internal/livekcp"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/internal/livekcp/livetest"
)

type probeSpec struct {
	Steps int32 `json:"steps,omitempty"`
}

type probeStatus struct {
	Phase string `json:"phase,omitempty"`

	Observed int32 `json:"observed,omitempty"`
}

type probe = livekcp.Object[probeSpec, probeStatus]

func TestLiveKcpstoreAgainstRealKCP(t *testing.T) {
	livetest.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cluster, err := livekcp.Start(ctx)
	if err != nil {
		t.Fatalf("start kcp: %v", err)
	}
	defer cluster.Stop()

	store, err := New(Options{Host: cluster.Server, RestConfig: cluster.Config})
	if err != nil {
		t.Fatal(err)
	}
	probes := Of[probe](store, livekcp.WidgetGVR)
	target := ref.New(cluster.ConsumerCluster, "default", "storage-probe")

	obj := livekcp.NewObject[probeSpec, probeStatus]("default", "storage-probe")
	obj.Spec.Steps = 3
	if err := livekcp.Seed(ctx, cluster.ConsumerCluster, probes, "default", "storage-probe", obj); err != nil {
		t.Fatalf("create: %v", err)
	}

	created, err := probes.Get(ctx, target)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if created.Metadata.UID == "" || created.Metadata.ResourceVersion == "" {
		t.Fatalf("the server must stamp identity and a resource version: %+v", created.Metadata)
	}
	if created.Spec.Steps != 3 {
		t.Fatalf("the spec must round trip: %+v", created.Spec)
	}

	listed, err := probes.List(ctx, cluster.ConsumerCluster)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].Metadata.Name != "storage-probe" {
		t.Fatalf("listed = %+v", listed)
	}

	patch, err := statuspatch.Merge(map[string]any{"phase": "Running", "observed": 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := probes.PatchStatus(ctx, target.WithResourceVersion(created.Metadata.ResourceVersion), patch); err != nil {
		t.Fatalf("patch status: %v", err)
	}
	patched, err := probes.Get(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if patched.Status.Phase != "Running" || patched.Status.Observed != 2 {
		t.Fatalf("status = %+v", patched.Status)
	}
	if patched.Spec.Steps != 3 {
		t.Fatal("a status patch must not touch the spec")
	}

	if err := probes.AddFinalizer(ctx, target, []string{"live.example.computer/held"}); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	finalizers, err := probes.Finalizers(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalizers) != 1 || finalizers[0] != "live.example.computer/held" {
		t.Fatalf("finalizers = %v", finalizers)
	}
	if err := probes.RemoveFinalizer(ctx, target, "live.example.computer/held"); err != nil {
		t.Fatalf("remove finalizer: %v", err)
	}
	remaining, err := probes.Finalizers(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("finalizers = %v, want none", remaining)
	}

	path, err := store.ClusterPath(ctx, cluster.ConsumerCluster)
	if err != nil {
		t.Fatalf("cluster path: %v", err)
	}
	if path != cluster.ConsumerCluster {
		t.Fatalf("path = %q, want %q", path, cluster.ConsumerCluster)
	}

	if err := probes.Delete(ctx, target); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := probes.Get(ctx, target); !IsNotFound(err) {
		t.Fatalf("get after delete = %v, want not found", err)
	}
}
