//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc/hostsvctest"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpapply"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

const liveSets = "provider,permission-inventory,denoruntime,market-identity"

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

func liveValues(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte("market-identity:\n  Workspace: root:socialweb-provider\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplyingTheProviderSetTwiceChangesNothingTheSecondTime(t *testing.T) {
	d, id, _ := upForApply(t)
	if d == nil {
		return
	}
	values := liveValues(t)
	var first, second, dry kcpapply.Report
	d.run(t, &first, "apply", "--set", liveSets, "--values", values, "--instance", id)
	if first.Changed() == 0 {
		t.Fatalf("the first apply into a fresh instance changed nothing: %+v", first)
	}
	d.run(t, &second, "apply", "--set", liveSets, "--values", values, "--instance", id)
	if n := second.Changed(); n != 0 {
		for _, p := range second.Phases {
			for _, c := range p.Changes {
				if c.Action != kcpapply.Unchanged {
					t.Errorf("second apply: %s %s/%s in %s was %s: %+v", c.Kind, c.Namespace, c.Name, p.Workspace, c.Action, c.Managers)
				}
			}
		}
		t.Fatalf("applying the same sets a second time changed %d objects", n)
	}
	d.run(t, &dry, "apply", "--set", liveSets, "--values", values, "--instance", id, "--dry-run")
	if !dry.DryRun || dry.Changed() != 0 {
		t.Fatalf("a dry run over an applied instance reports %d changes (dryRun=%v)", dry.Changed(), dry.DryRun)
	}
	for _, p := range dry.Phases {
		for _, c := range p.Changes {
			if len(c.Managers) != 0 {
				t.Errorf("dry run: %s %s moves managers %+v", c.Kind, c.Name, c.Managers)
			}
		}
	}
}

func TestAChangedAPIResourceSchemaIsRefusedNotPatched(t *testing.T) {
	d, _, up := upForApply(t)
	if d == nil {
		return
	}
	cfg, err := restConfigFromKubeconfig(up.AdminKubeconfig)
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
