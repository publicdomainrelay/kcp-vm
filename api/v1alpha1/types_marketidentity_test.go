package v1alpha1

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var secretFieldName = regexp.MustCompile(`(?i)(token|jwt|password|secret|private|jwk|dpop|code|nonce|credential|key$|keys$)`)

func jsonFieldNames(t reflect.Type, prefix string, seen map[reflect.Type]bool, out *[]string) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.PkgPath() != reflect.TypeOf(MarketIdentity{}).PkgPath() || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := prefix + "." + name
		*out = append(*out, path)
		jsonFieldNames(f.Type, path, seen, out)
	}
}

func TestTheMarketIdentitySchemaHasNoSecretField(t *testing.T) {
	var names []string
	jsonFieldNames(reflect.TypeOf(MarketIdentitySpec{}), "spec", map[reflect.Type]bool{}, &names)
	jsonFieldNames(reflect.TypeOf(MarketIdentityStatus{}), "status", map[reflect.Type]bool{}, &names)
	if len(names) < 10 {
		t.Fatalf("walked only %d fields of MarketIdentity: %v", len(names), names)
	}
	for _, path := range names {
		leaf := path[strings.LastIndex(path, ".")+1:]
		if strings.HasSuffix(leaf, "Ref") {
			continue
		}
		if secretFieldName.MatchString(leaf) {
			t.Errorf("MarketIdentity field %s names secret material; keys, tokens, DPoP keys and codes live in the application's OpenBao and the object holds only a *Ref", path)
		}
	}
	for _, ref := range []string{"spec.keysRef", "spec.sessionRef", "status.sessionRef"} {
		found := false
		for _, path := range names {
			found = found || path == ref
		}
		if !found {
			t.Errorf("MarketIdentity has no %s vault reference", ref)
		}
	}
}

func TestMarketIdentityVaultRefCarriesOnlyALocation(t *testing.T) {
	ty := reflect.TypeOf(VaultRef{})
	var fields []string
	for i := 0; i < ty.NumField(); i++ {
		fields = append(fields, strings.Split(ty.Field(i).Tag.Get("json"), ",")[0])
	}
	if !reflect.DeepEqual(fields, []string{"mount", "path"}) {
		t.Fatalf("VaultRef fields = %v, want [mount path]", fields)
	}
}

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

func TestMarketIdentityModesAndActionsAreThePlansLiterals(t *testing.T) {
	modes := []MarketIdentityMode{MarketIdentityModeOAuthQr, MarketIdentityModeOAuthBrowser, MarketIdentityModePdsAccount, MarketIdentityModeLocalRepo, MarketIdentityModeOAuthDelegated}
	if got := []string{string(modes[0]), string(modes[1]), string(modes[2]), string(modes[3]), string(modes[4])}; !reflect.DeepEqual(got, []string{"oauthQr", "oauthBrowser", "pdsAccount", "localRepo", "oauthDelegated"}) {
		t.Fatalf("modes = %v", got)
	}
	actions := []PendingActionKind{PendingActionScanQr, PendingActionVisitUrl, PendingActionApproveAssociation, PendingActionSignInAgain}
	if got := []string{string(actions[0]), string(actions[1]), string(actions[2]), string(actions[3])}; !reflect.DeepEqual(got, []string{"scanQr", "visitUrl", "approveAssociation", "signInAgain"}) {
		t.Fatalf("pending action kinds = %v", got)
	}
	if MarketIdentityPhaseSessionExpired != "SessionExpired" || MarketIdentityPhaseReady != "Ready" {
		t.Fatalf("phases drifted: %s %s", MarketIdentityPhaseSessionExpired, MarketIdentityPhaseReady)
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
