package apigen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

const repoRoot = "../.."

func mustModel(t *testing.T) *Model {
	t.Helper()
	m, err := BuildV1alpha1(repoRoot)
	if err != nil {
		t.Fatalf("build the model from api/v1alpha1: %v", err)
	}
	return m
}

func TestEveryKindTheSchemeRegistersHasAnAPIResource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, r := range v1alpha1.APIResources {
		listed[r.Kind] = true
	}
	for kind := range scheme.KnownTypes(v1alpha1.GroupVersion) {
		if strings.HasSuffix(kind, "List") {
			continue
		}
		if !listed[kind] {
			t.Errorf("api/v1alpha1 registers %s and APIResources does not list it, so no "+
				"APIResourceSchema and no TypeScript type are generated for it", kind)
		}
	}
}

func TestEnumValuesAreTheConstantsInDeclarationOrder(t *testing.T) {
	m := mustModel(t)
	want := map[string][]string{
		"NodeClaimPhase":      {"Pending", "Bidding", "Provisioning", "Joining", "Running", "Failed", "Released", "Lost", "Adopting"},
		"ContractPhase":       {"requested", "rfp_submitted", "winner_selected", "accepted", "receipt", "network", "released"},
		"ClusterLinkPhase":    {"Pending", "Listening", "Failed"},
		"ClusterLinkService":  {"apiserver", "openbao"},
		"ClusterPhase":        {"Initializing", "Ready", "Unavailable", "Draining"},
		"NodePhase":           {"Pending", "Running", "Draining"},
		"IdentityScope":       {"Account", "Cluster"},
		"IdentityState":       {"Active", "Revoked", "RevocationPending", "ColdDeferred"},
		"ContractOutcome":     {"no_bids", "bids_denied", "policy_rejected", "returned", "receipt_invalid", "network_timeout", "complete"},
		"MarketIdentityMode":  {"oauthQr", "oauthBrowser", "pdsAccount", "localRepo", "oauthDelegated"},
		"AssociationMode":     {"RequireApproval", "Skip"},
		"MarketIdentityPhase": {"Pending", "AwaitingScan", "AwaitingAuthorization", "AwaitingApproval", "Ready", "SessionExpired", "Failed"},
		"PendingActionKind":   {"scanQr", "visitUrl", "approveAssociation", "signInAgain"},
		"ComputeVMPhase":      {"Pending", "Preparing", "Booting", "Running", "Releasing", "Lost", "Failed"},
		"ComputeVMLostReason": {"HostRebooted", "UnitGone", "UnitFailed", "OOMKilled", "VMMExited", "IdentityMismatch"},
	}
	got := map[string][]string{}
	for _, e := range m.Enums {
		got[e.Name] = e.Values
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enums:\n got %v\nwant %v", got, want)
	}
}

func TestEveryTypedStringConstantIsAnEnumMember(t *testing.T) {
	m := mustModel(t)
	members := map[string]map[string]bool{}
	for _, e := range m.Enums {
		members[e.Name] = map[string]bool{}
		for _, v := range e.Values {
			members[e.Name][v] = true
		}
	}
	fset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(APISourceDir(repoRoot), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		{
			ast.Inspect(f, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok || vs.Type == nil {
					return true
				}
				ident, ok := vs.Type.(*ast.Ident)
				if !ok || members[ident.Name] == nil {
					return true
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok {
						t.Errorf("%s: a %s constant is not a string literal, so the generator cannot see it",
							fset.Position(v.Pos()), ident.Name)
						continue
					}
					if !members[ident.Name][strings.Trim(lit.Value, "\"")] {
						t.Errorf("%s: %s %s is not in the generated enum", fset.Position(v.Pos()), ident.Name, lit.Value)
					}
				}
				return true
			})
		}
	}
}

func TestRequiredFollowsOmitemptyAndTheOptionalTag(t *testing.T) {
	m := mustModel(t)
	cases := map[string][]string{
		"ClusterSpec":         {"account"},
		"NodeSpec":            {"cluster"},
		"AtprotoIdentitySpec": {"did", "scope"},
		"NodeClaimSpec": {"cluster", "nodeName", "ownerDid", "apiserverPort", "advertiseAddress",
			"tokenFile", "nodePasswordFile", "serverCAFile"},
		"NodeClaimStatus":   nil,
		"NodeClaimContract": nil,
		"RecordRef":         {"uri", "cid"},
		"ContractRelease":   {"event", "payload", "ok"},
	}
	for name, want := range cases {
		s, ok := m.Named[name]
		if !ok {
			t.Errorf("no generated type %s", name)
			continue
		}
		if got := s.Required(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s required = %v, want %v", name, got, want)
		}
	}
}

func TestTheRevisionLockRefusesAChangedSpecUnderAnOldName(t *testing.T) {
	lock := RevisionLock{"v1alpha1-1.nodeclaims.socialweb.computer": "sha256:old"}
	if err := lock.Admit("v1alpha1-1.nodeclaims.socialweb.computer", "sha256:old"); err != nil {
		t.Fatalf("an unchanged spec was refused: %v", err)
	}
	err := lock.Admit("v1alpha1-1.nodeclaims.socialweb.computer", "sha256:new")
	if err == nil || !strings.Contains(err.Error(), "Bump Revision for nodeclaims") {
		t.Fatalf("a changed spec under the old name was admitted: %v", err)
	}
	if err := lock.Admit("v1alpha1-2.nodeclaims.socialweb.computer", "sha256:new"); err != nil {
		t.Fatalf("a new revision was refused: %v", err)
	}
	if len(lock) != 2 {
		t.Fatalf("the lock forgot a revision: %v", lock)
	}
}

func TestUpperSnake(t *testing.T) {
	for in, want := range map[string]string{
		"FinalizerNodeClaim":       "FINALIZER_NODE_CLAIM",
		"GroupName":                "GROUP_NAME",
		"Version":                  "VERSION",
		"ConditionTenantPathShut":  "CONDITION_TENANT_PATH_SHUT",
		"FinalizerAtprotoIdentity": "FINALIZER_ATPROTO_IDENTITY",
		"NodeClaimPhase":           "NODE_CLAIM_PHASE",
		"URLPath":                  "URL_PATH",
	} {
		if got := UpperSnake(in); got != want {
			t.Errorf("UpperSnake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTheGeneratedFilesCoverEveryResourceAndExport(t *testing.T) {
	outputs, err := Generate(repoRoot)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var paths []string
	for _, o := range outputs {
		paths = append(paths, o.Path)
	}
	var want []string
	if _, err := os.Stat(filepath.Join(repoRoot, computeVMSiblingRepo)); err == nil {
		want = append(want, computeVMTypeScript)
		fixtures, err := filepath.Glob(filepath.Join(computeVMFixtureDir, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fixtures {
			want = append(want, "../hono-compute-provider/test/fixtures/computevm/"+filepath.Base(f))
		}
	}
	want = append(want,
		"deploy/apiresourceschema-revisions.json",
		"deploy/atprotoidentity-apiexport.yaml",
		"deploy/atprotoidentity-apiresourceschema.yaml",
		"deploy/cluster-apiexport.yaml",
		"deploy/cluster-apiresourceschema.yaml",
		"deploy/clusterlink-apiresourceschema.yaml",
		"deploy/computevm-apiexport.yaml",
		"deploy/computevm-apiresourceschema.yaml",
		"deploy/marketidentity-apiexport.yaml",
		"deploy/marketidentity-apiresourceschema.yaml",
		"deploy/node-apiresourceschema.yaml",
		"deploy/nodeclaim-apiresourceschema.yaml",
		TSPackageDir+"/mod.ts",
	)
	sort.Strings(want)
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("generated paths:\n got %v\nwant %v", paths, want)
	}
}
