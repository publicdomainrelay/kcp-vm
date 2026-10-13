//go:build linux

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc/hostsvctest"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpapply"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

func upForApply(t *testing.T) (*liveDev, string, upReport) {
	t.Helper()
	d := openLiveDev(t)
	if d == nil {
		return nil, "", upReport{}
	}
	id := "t" + hostsvctest.RandomKey() + "-apply"
	t.Cleanup(func() { d.runE(nil, "dev", "down", "--instance", id) })
	return d, id, d.up(t, id)
}

func TestAChangedAPIResourceSchemaIsRefusedNotPatched(t *testing.T) {
	d, _, up := upForApply(t)
	if d == nil {
		return
	}
	cfg, err := kcpserver.FrontRestConfig(up.AdminKubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := manifests.Load()
	if err != nil {
		t.Fatal(err)
	}
	set, _ := ix.Set("provider")
	ap, err := kcpapply.New(cfg, kcpapply.Options{WaitTimeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := ap.Apply(ctx, set, &manifests.None{}); err != nil {
		t.Fatal(err)
	}
	r, err := set.RenderPhase(set.Phases[1], &manifests.None{})
	if err != nil {
		t.Fatal(err)
	}
	schema := r.Objects[0]
	before := schema.Unstructured.DeepCopy()
	changed := schema.Unstructured.DeepCopy()
	versions, _, _ := unstructured.NestedSlice(changed.Object, "spec", "versions")
	versions[0].(map[string]any)["served"] = false
	if err := unstructured.SetNestedSlice(changed.Object, versions, "spec", "versions"); err != nil {
		t.Fatal(err)
	}
	_, err = ap.ApplyObjects(ctx, r.Workspace, []manifests.Object{{Path: schema.Path, Typed: schema.Typed, Unstructured: changed}})
	if !errors.Is(err, kcpapply.ErrSchemaChanged) {
		t.Fatalf("a changed %s was not refused: %v", before.GetName(), err)
	}
	report, err := ap.ApplyObjects(ctx, r.Workspace, []manifests.Object{schema})
	if err != nil {
		t.Fatalf("the unchanged schema after the refusal: %v", err)
	}
	if len(report) != 1 || report[0].Action != kcpapply.Unchanged {
		t.Fatalf("the live schema moved after a refused change: %+v", report)
	}
}
