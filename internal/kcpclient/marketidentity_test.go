package kcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/appcli"
)

var _ appcli.MarketIdentityClient = (*MarketIdentities)(nil)

func identityPath() string {
	return "/clusters/" + homePath + "/apis/socialweb.computer/v1alpha1/marketidentities/self"
}

func awaitingApproval() v1alpha1.MarketIdentity {
	return v1alpha1.MarketIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: "self", ResourceVersion: "7"},
		Spec:       v1alpha1.MarketIdentitySpec{Mode: v1alpha1.MarketIdentityModeLocalRepo, ApprovedAssociations: []string{"did:plc:earlier"}},
		Status: v1alpha1.MarketIdentityStatus{
			Phase:         v1alpha1.MarketIdentityPhaseAwaitingApproval,
			PendingAction: &v1alpha1.MarketIdentityPendingAction{Kind: v1alpha1.PendingActionApproveAssociation, CallerDid: "did:plc:alice"},
		},
	}
}

func TestApprovingAnAssociationPatchesOnlyTheSpecListUnderTheReadVersion(t *testing.T) {
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch {
		case req.Method == http.MethodGet && req.Path == identityPath():
			return http.StatusOK, awaitingApproval()
		case req.Method == http.MethodPatch && req.Path == identityPath():
			return http.StatusOK, awaitingApproval()
		}
		return 0, nil
	}}
	ids, err := newTestClient(t, f).MarketIdentities(homePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ids.ApproveAssociation(context.Background(), "self", "did:plc:alice"); err != nil {
		t.Fatal(err)
	}
	var patch *request
	for _, r := range f.seen() {
		if r.Method == http.MethodPatch {
			patch = &r
		}
	}
	if patch == nil {
		t.Fatalf("no patch sent: %+v", f.seen())
	}
	var ops []map[string]any
	if err := json.Unmarshal([]byte(patch.Body), &ops); err != nil {
		t.Fatalf("patch body %q: %v", patch.Body, err)
	}
	if len(ops) != 2 || ops[0]["op"] != "test" || ops[0]["path"] != "/metadata/resourceVersion" || ops[0]["value"] != "7" {
		t.Fatalf("patch does not test the read resourceVersion: %v", ops)
	}
	list, _ := ops[1]["value"].([]any)
	if ops[1]["path"] != "/spec/approvedAssociations" || len(list) != 2 || list[0] != "did:plc:earlier" || list[1] != "did:plc:alice" {
		t.Fatalf("patch does not append the caller to spec.approvedAssociations: %v", ops)
	}
}

func TestReadingAMissingMarketIdentityIsAnError(t *testing.T) {
	f := &fakeKCP{t: t, respond: func(req request) (int, any) { return notFound() }}
	ids, err := newTestClient(t, f).MarketIdentities(homePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ids.GetMarketIdentity(context.Background(), "self"); err == nil {
		t.Fatal("a missing identity read as nil without an error; the renderer would spin")
	}
}
