package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestMarketIdentityIsRegisteredInTheScheme(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	for kind, obj := range map[string]runtime.Object{"MarketIdentity": &MarketIdentity{}, "MarketIdentityList": &MarketIdentityList{}} {
		gvks, _, err := scheme.ObjectKinds(obj)
		if err != nil || len(gvks) == 0 || gvks[0].Kind != kind || gvks[0].Group != GroupName {
			t.Fatalf("%s: kinds %v, err %v", kind, gvks, err)
		}
	}
}

func TestDeepCopyDoesNotAliasMarketIdentityListsRefsOrAction(t *testing.T) {
	orig := &MarketIdentity{
		Spec: MarketIdentitySpec{
			Mode:                 MarketIdentityModeLocalRepo,
			ApprovedAssociations: []string{"did:plc:alice"},
			SessionRef:           &VaultRef{Mount: "app", Path: "identity/session"},
		},
		Status: MarketIdentityStatus{
			PendingAction: &MarketIdentityPendingAction{Kind: PendingActionApproveAssociation, CallerDid: "did:plc:alice"},
			Associations:  []string{"did:plc:alice"},
			SessionRef:    &VaultRef{Mount: "app", Path: "identity/session"},
			Conditions:    []metav1.Condition{{Type: ConditionSignedIn, Status: metav1.ConditionTrue}},
		},
	}
	dup := orig.DeepCopy()
	dup.Spec.ApprovedAssociations[0] = "x"
	dup.Spec.SessionRef.Path = "x"
	dup.Status.PendingAction.CallerDid = "x"
	dup.Status.Associations[0] = "x"
	dup.Status.SessionRef.Path = "x"
	dup.Status.Conditions[0].Type = "x"
	if orig.Spec.ApprovedAssociations[0] != "did:plc:alice" || orig.Spec.SessionRef.Path != "identity/session" ||
		orig.Status.PendingAction.CallerDid != "did:plc:alice" || orig.Status.Associations[0] != "did:plc:alice" ||
		orig.Status.SessionRef.Path != "identity/session" || orig.Status.Conditions[0].Type != ConditionSignedIn {
		t.Fatalf("DeepCopy aliased the original: %+v", orig)
	}
}
