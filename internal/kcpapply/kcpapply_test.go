package kcpapply

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func TestTheWorkspaceIsAddressedByClustersPath(t *testing.T) {
	a, err := New(&rest.Config{Host: "https://127.0.0.1:6443/clusters/root:x"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.ForWorkspace("root:socialweb-provider").Host; got != "https://127.0.0.1:6443/clusters/root:socialweb-provider" {
		t.Fatalf("workspace host %s", got)
	}
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("a nil rest.Config was accepted")
	}
}

func TestSchemaSpecComparisonIgnoresServerDefaultsAndCatchesChanges(t *testing.T) {
	want := map[string]any{"group": "g", "versions": []any{map[string]any{"name": "v1", "served": true, "schema": map[string]any{"type": "object"}}}}
	live := map[string]any{"group": "g", "scope": "Cluster", "versions": []any{map[string]any{"name": "v1", "served": true, "storage": true, "schema": map[string]any{"type": "object"}}}}
	if at, same := derivative(want, live, "spec"); !same {
		t.Fatalf("server-filled fields read as a change at %s", at)
	}
	changed := map[string]any{"group": "g", "versions": []any{map[string]any{"name": "v1", "served": false, "schema": map[string]any{"type": "object"}}}}
	at, same := derivative(changed, live, "spec")
	if same || !strings.Contains(at, "served") {
		t.Fatalf("a changed served flag compared equal (%s)", at)
	}
	if _, same := derivative(map[string]any{"n": int64(3)}, map[string]any{"n": float64(3)}, "spec"); !same {
		t.Fatal("3 and 3.0 compared unequal")
	}
}

func managed(entries ...metav1.ManagedFieldsEntry) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": "a"}}}
	u.SetManagedFields(entries)
	return u
}

func entry(manager, op, fields string) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{Manager: manager, Operation: metav1.ManagedFieldsOperationType(op),
		FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(fields)}}
}

func TestManagerChangesNameEveryFieldWhoseOwnerMoves(t *testing.T) {
	before := managed(entry("other", "Apply", `{"f:metadata":{"f:labels":{"f:a":{}}},"f:spec":{"f:x":{}}}`))
	after := managed(
		entry("other", "Apply", `{"f:metadata":{"f:labels":{"f:a":{}}}}`),
		entry(FieldManager, "Apply", `{"f:spec":{"f:x":{},"f:y":{}}}`),
	)
	got := map[string]FieldChange{}
	for _, c := range managerChanges(before, after) {
		got[c.Field] = c
	}
	if len(got) != 2 {
		t.Fatalf("changes %v; want spec.x moved and spec.y new", got)
	}
	if c := got["f:spec.f:x"]; strings.Join(c.From, ",") != "other (Apply)" || strings.Join(c.To, ",") != FieldManager+" (Apply)" {
		t.Errorf("spec.x: %+v", c)
	}
	if c := got["f:spec.f:y"]; len(c.From) != 0 || len(c.To) != 1 {
		t.Errorf("spec.y: %+v", c)
	}
	if n := len(managerChanges(after, after)); n != 0 {
		t.Errorf("an unchanged object reports %d manager changes", n)
	}
	if n := len(managerChanges(nil, after)); n != 3 {
		t.Errorf("a created object reports %d manager changes, want its 3 fields", n)
	}
}

func TestAKubectlAppliedObjectIsNeverAdopted(t *testing.T) {
	for _, m := range []string{"kubectl-client-side-apply", "kubectl", "kubectl-edit"} {
		if err := refuseKubectl(managed(entry(m, "Update", `{"f:spec":{}}`))); !errors.Is(err, ErrKubectlManaged) {
			t.Errorf("an object managed by %s: %v", m, err)
		}
	}
	if err := refuseKubectl(managed(entry(FieldManager, "Apply", `{}`), entry("kcp-workspace-controller", "Update", `{}`))); err != nil {
		t.Errorf("an object we and a controller manage: %v", err)
	}
}
