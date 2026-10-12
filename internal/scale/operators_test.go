package scale

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func testOperators() Operators {
	return Operators{
		{Group: "inside.example.com", Kind: "Widget", ReadyCondition: "Ready", InCluster: true},
		{Group: "beside.example.com", Kind: "Gadget", ReadyCondition: "Ready", InCluster: false},
	}
}

func widget(name string, generation int64, conditions ...CustomResourceCondition) CustomResource {
	return CustomResource{Group: "inside.example.com", Kind: "Widget", Namespace: "ns", Name: name,
		Generation: generation, Conditions: conditions}
}

func TestACustomResourceCountsOnlyWhileItsInClusterOperatorHasNotReconciledIt(t *testing.T) {
	ready := CustomResourceCondition{Type: "Ready", Status: "True"}
	notReady := CustomResourceCondition{Type: "Ready", Status: "False"}
	stale := CustomResourceCondition{Type: "Ready", Status: "True", ObservedGeneration: 1}
	cases := []struct {
		name    string
		cr      CustomResource
		pending bool
	}{
		{"an in-cluster kind with no status yet", widget("new", 1), true},
		{"an in-cluster kind reading Ready False", widget("failing", 1, notReady), true},
		{"an in-cluster kind reading Ready True", widget("done", 1, ready), false},
		{"an in-cluster kind Ready for an older generation", widget("edited", 2, stale), true},
		{"an in-cluster kind whose status lags the generation", func() CustomResource {
			cr := widget("lagging", 3, ready)
			cr.ObservedGeneration = 2
			return cr
		}(), true},
		{"an in-cluster kind being deleted behind a finalizer", func() CustomResource {
			cr := widget("leaving", 1, ready)
			cr.Deleting, cr.Finalizers = true, 1
			return cr
		}(), true},
		{"an in-cluster kind being deleted with no finalizer left", func() CustomResource {
			cr := widget("gone", 1)
			cr.Deleting = true
			return cr
		}(), false},
		{"a kind whose operator runs beside the control plane", CustomResource{Group: "beside.example.com",
			Kind: "Gadget", Name: "g", Generation: 1, Conditions: []CustomResourceCondition{notReady}}, false},
		{"a kind no operator of ours reconciles", CustomResource{Group: "other.example.com", Kind: "Widget",
			Name: "x", Generation: 1}, false},
	}
	for _, tc := range cases {
		got := len(testOperators().Unreconciled([]CustomResource{tc.cr})) == 1
		if got != tc.pending {
			t.Errorf("%s: counted %v, want %v", tc.name, got, tc.pending)
		}
	}
}

func TestAnUnreconciledCustomResourceAsksForOneNodeAndAReconciledOneForNone(t *testing.T) {
	pending := Observation{UnreconciledCustomResources: testOperators().Unreconciled([]CustomResource{widget("w", 1)})}
	if !Intent(pending) || Target(pending, Options{}) != 1 {
		t.Fatalf("an unreconciled in-cluster CR gave intent %v target %d, want true and 1: its operator "+
			"runs in the tenant cluster and needs a node to reconcile it", Intent(pending), Target(pending, Options{}))
	}
	reconciled := Observation{UnreconciledCustomResources: testOperators().Unreconciled([]CustomResource{
		widget("w", 1, CustomResourceCondition{Type: "Ready", Status: "True"})})}
	if Intent(reconciled) {
		t.Fatal("a reconciled CR is intent; it would hold a node for an object nothing still works on")
	}
	narrowed := Narrow(pending, nil, Baseline{})
	if !Intent(narrowed) {
		t.Fatal("Narrow dropped the unreconciled CRs")
	}
}

func TestCustomResourceOfReadsTheFieldsTheRuleNeeds(t *testing.T) {
	now := metav1.Now()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "inside.example.com/v1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"namespace": "ns", "name": "w", "generation": int64(4), "finalizers": []any{"x"},
		},
		"status": map[string]any{
			"observedGeneration": int64(3),
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(3)},
				"not a condition",
			},
		},
	}}
	u.SetDeletionTimestamp(&now)
	cr := CustomResourceOf(u)
	if cr.Group != "inside.example.com" || cr.Kind != "Widget" || cr.Namespace != "ns" || cr.Name != "w" ||
		cr.Generation != 4 || cr.ObservedGeneration != 3 || !cr.Deleting || cr.Finalizers != 1 ||
		len(cr.Conditions) != 1 || cr.Conditions[0] != (CustomResourceCondition{Type: "Ready", Status: "True", ObservedGeneration: 3}) {
		t.Fatalf("CustomResourceOf = %+v", cr)
	}
}
