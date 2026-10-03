package v1alpha1

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func ptr(v int64) *int64 { return &v }

func TestNodeExpiryDistinguishesUnlimitedFromAbsent(t *testing.T) {
	cases := []struct {
		name      string
		expiry    *int64
		wantKey   bool
		wantValue float64
	}{
		{"absent means no producer, and the gate refuses", nil, false, 0},
		{"zero means UNLIMITED, and the gate proceeds", ptr(0), true, 0},
		{"a deadline is an absolute unix second", ptr(1758500000), true, 1758500000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []any{
				&ClusterStatus{NodeExpiry: tc.expiry},
				&NodeStatus{NodeExpiry: tc.expiry},
			} {
				raw, err := json.Marshal(status)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var got map[string]any
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				value, present := got["nodeExpiry"]
				if present != tc.wantKey {
					t.Fatalf("%T: nodeExpiry present=%v, want %v -- a plain int64 would make "+
						"UNLIMITED indistinguishable from ABSENT, and the teardown gate refuses "+
						"when the value is unknown, so every unlimited cluster would be undeletable",
						status, present, tc.wantKey)
				}
				if present && value != tc.wantValue {
					t.Fatalf("%T: nodeExpiry=%v, want %v", status, value, tc.wantValue)
				}
			}
		})
	}
}

func TestMountContractFieldNamesAreNotGoIdiomatic(t *testing.T) {
	raw, err := json.Marshal(&ClusterStatus{URL: "https://example.invalid", Phase: ClusterReady})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["URL"]; !ok {
		t.Fatalf("ClusterStatus must serialize the field as URL (capitals); kcp reads it by "+
			"that name and a rename to idiomatic Go casing silently breaks every mount. got: %v", got)
	}
	if _, ok := got["phase"]; !ok {
		t.Fatalf("ClusterStatus must serialize phase for kcp's Ready check. got: %v", got)
	}
}

func TestOnlyTheLiteralReadySatisfiesAMount(t *testing.T) {
	for _, p := range []ClusterPhase{
		ClusterInitializing, ClusterUnavailable, ClusterDraining,
	} {
		if p == ClusterReady {
			t.Fatalf("%q must not be the Ready literal", p)
		}
	}
	if ClusterReady != "Ready" {
		t.Fatalf("kcp compares against the literal %q, not %q", "Ready", ClusterReady)
	}
	phases := map[ClusterPhase]bool{
		ClusterInitializing: true, ClusterReady: true,
		ClusterUnavailable: true, ClusterDraining: true,
	}
	if len(phases) != 4 {
		t.Fatalf("two phases collided: %v", phases)
	}
}

func TestSchemeRoundTripsEveryType(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	for _, want := range []struct {
		kind string
		obj  runtime.Object
	}{
		{"Cluster", &Cluster{}},
		{"ClusterList", &ClusterList{}},
		{"Node", &Node{}},
		{"NodeList", &NodeList{}},
		{"AtprotoIdentity", &AtprotoIdentity{}},
		{"AtprotoIdentityList", &AtprotoIdentityList{}},
	} {
		gvks, _, err := scheme.ObjectKinds(want.obj)
		if err != nil {
			t.Fatalf("ObjectKinds(%s): %v", want.kind, err)
		}
		if len(gvks) == 0 || gvks[0].Kind != want.kind {
			t.Fatalf("kind round-trip: got %v, want %s", gvks, want.kind)
		}
		if gvks[0].Group != GroupName {
			t.Fatalf("%s registered under group %q, want %q", want.kind, gvks[0].Group, GroupName)
		}
	}
}

func TestDeepCopyDoesNotAliasNodeExpiryOrConditions(t *testing.T) {
	orig := &Cluster{
		Status: ClusterStatus{
			NodeExpiry: ptr(42),
			Conditions: []metav1.Condition{{Type: ConditionTeardownBlocked, Status: metav1.ConditionTrue}},
		},
	}
	dup := orig.DeepCopy()
	*dup.Status.NodeExpiry = 99
	dup.Status.Conditions[0].Type = "mutated"
	if *orig.Status.NodeExpiry != 42 {
		t.Fatalf("DeepCopy aliased nodeExpiry: original changed to %d", *orig.Status.NodeExpiry)
	}
	if orig.Status.Conditions[0].Type != ConditionTeardownBlocked {
		t.Fatalf("DeepCopy aliased conditions: original changed to %q", orig.Status.Conditions[0].Type)
	}
}
