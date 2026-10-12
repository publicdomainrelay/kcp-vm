package gatewaymain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/atprotoauth"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/xrpcserver"
)

const answeringCaller = "did:plc:abcdefghijklmnopqrstuvwx"

type signingDirectory struct{ keys map[string]atcrypto.PublicKey }

func (d signingDirectory) LookupDID(_ context.Context, did syntax.DID) (*identity.Identity, error) {
	pk, ok := d.keys[did.String()]
	if !ok {
		return nil, identity.ErrDIDNotFound
	}
	return &identity.Identity{DID: did, Keys: map[string]identity.VerificationMethod{
		"atproto": {Type: "Multikey", PublicKeyMultibase: pk.Multibase()},
	}}, nil
}

func (d signingDirectory) LookupHandle(context.Context, syntax.Handle) (*identity.Identity, error) {
	return nil, identity.ErrHandleNotFound
}

func (d signingDirectory) Lookup(context.Context, syntax.AtIdentifier) (*identity.Identity, error) {
	return nil, identity.ErrDIDNotFound
}

func (d signingDirectory) Purge(context.Context, syntax.AtIdentifier) error { return nil }

func TestBuildDepsWiresTheDeviceKeyGranterFromTheRealImplementation(t *testing.T) {
	cfg := testConfig(t)
	cfg.KCPKubeconfig = writeKubeconfig(t)
	cfg.MintKubeconfigs = writeMintKubeconfigs(t, "live")
	cfg.KCPHost = deadKCPHost

	deps, err := buildDeps(cfg)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	if deps.Store == nil {
		t.Fatal("buildDeps supplied no store, so Build serves nothing at all")
	}

	_, err = deps.Store.Kubeconfig(context.Background(), gateway.Caller(answeringCaller), "c", 0)
	if err == nil {
		t.Fatal("getKubeconfig answered a kubeconfig with no kcp to reach; this test's " +
			"premise is that the host is unreachable, so a nil error means the premise broke")
	}
	if errors.Is(err, kcpclient.ErrNoDeviceKey) {
		t.Fatal("the store buildDeps builds carries NO device-key granter: getKubeconfig is " +
			"declared on the XRPC surface (section 6, \"the credential the user runs kubectl " +
			"with\") and returns ErrNoDeviceKey to every caller it will ever receive, while " +
			"being routed, documented and unit-tested. internal/devicekey is the " +
			"implementation and main.buildDeps is where it is handed over -- a nil here " +
			"means the composition root skipped that construction. Pass --verbose to see the " +
			"guard: kcpclient.Client.Kubeconfig's first statement is " +
			"`if c.deviceKeys == nil { return \"\", ErrNoDeviceKey }`")
	}
	if !strings.Contains(err.Error(), "devicekey: bind") {
		t.Fatalf("the store answered %v. A wired granter generates the device key and then "+
			"reaches its Binder, and this host is unreachable, so the failure must be the "+
			"binder's -- anything else means the granter was skipped, not merely unreachable", err)
	}
}

const deadKCPHost = "https://127.0.0.1:1"

func TestEveryVerbTheLexiconsDeclareIsAnswerableWithTheRealDeps(t *testing.T) {
	cfg := testConfig(t)
	cfg.KCPKubeconfig = writeInsecureKubeconfig(t)
	cfg.MintKubeconfigs = writeMintKubeconfigs(t, "c")

	kcp := newFakeKCP(answeringCaller, "c")
	srv := httptest.NewTLSServer(kcp)
	defer srv.Close()
	cfg.KCPHost = srv.URL

	priv, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatalf("GeneratePrivateKeyP256: %v", err)
	}
	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}

	deps, err := buildDeps(cfg)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}

	deps.Directory = signingDirectory{keys: map[string]atcrypto.PublicKey{answeringCaller: pub}}

	g, err := Build(cfg, deps, quiet())
	if err != nil {
		t.Fatalf("Build refused the deps buildDeps returned: %v", err)
	}
	handler := g.Handler()

	for _, verb := range answerableVerbs() {
		t.Run(verb.name, func(t *testing.T) {
			method := http.MethodPost
			if verb.query {
				method = http.MethodGet
			}
			nsid := xrpcserver.NSIDPrefix + verb.name
			rec := request(t, handler, method, xrpcserver.XRPCPrefix+nsid,
				"Bearer "+signToken(t, priv, cfg.Audiences[0], nsid), verb.body)

			switch rec.Code {
			case http.StatusNotFound:
				t.Fatalf("%s is not routed: the surface is missing a verb the lexicons declare", verb.name)
			case http.StatusUnauthorized:
				t.Fatalf("%s answered Unauthorized to a token signed for its own NSID and this "+
					"deployment's first audience -- the door, not the verb, is what failed here", verb.name)
			case http.StatusOK:
			default:
				t.Fatalf("%s answered %d over the deps buildDeps returns: %s\n\n"+
					"A verb on the surface that cannot answer is this document's recurring "+
					"defect -- a control that verifies existence while the thing it names does "+
					"not work. Routing is not function: TestEveryVerbIsBound asks for a "+
					"non-404 and an unauthenticated request is 401 whether or not the handler "+
					"works, so both pass for a verb whose every answer is a refusal.",
					verb.name, rec.Code, rec.Body)
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s answered a body that is not JSON: %v (%s)", verb.name, err, rec.Body)
			}
			verb.check(t, cfg, body)
		})
	}

	if len(kcp.seen()) == 0 {
		t.Fatal("the verbs answered without a single request reaching kcp, so nothing here " +
			"exercised the store buildDeps actually builds")
	}
}

func answerableVerbs() []struct {
	name  string
	query bool
	body  string
	check func(*testing.T, Config, map[string]any)
} {
	return []struct {
		name  string
		query bool
		body  string
		check func(*testing.T, Config, map[string]any)
	}{
		{"getKubeconfig", false, `{"name":"c"}`, func(t *testing.T, cfg Config, body map[string]any) {
			doc, _ := body["kubeconfig"].(string)
			if doc == "" {
				t.Fatalf("getKubeconfig answered no kubeconfig: %v", body)
			}
			if !strings.Contains(doc, cfg.DeviceKeyExecCommand) {
				t.Fatalf("the kubeconfig does not name the configured helper %q, so the "+
					"credential it carries is not the one the deployment chose to run:\n%s",
					cfg.DeviceKeyExecCommand, doc)
			}
			if doc, _ := body["instance"].(string); doc != "1" {
				t.Fatalf("instance = %v, want the cluster's status.instance", body["instance"])
			}
		}},
		{"createCluster", false, `{"name":"c"}`, nonEmptyPhase},
		{"controlPlaneUp", false, `{"name":"c"}`, nonEmptyPhase},
		{"controlPlaneDown", false, `{"name":"c"}`, nonEmptyPhase},
		{"deleteCluster", false, `{"name":"c"}`, nonEmptyPhase},
		{"listClusters", true, "", func(t *testing.T, _ Config, body map[string]any) {
			if _, ok := body["clusters"].([]any); !ok {
				t.Fatalf("listClusters answered no clusters array: %v", body)
			}
		}},
		{"revokeIdentity", false, `{}`, func(t *testing.T, _ Config, body map[string]any) {
			entries, ok := body["revoked"].([]any)
			if !ok || len(entries) == 0 {
				t.Fatalf("revokeIdentity answered no revocation plan: %v", body)
			}
		}},
	}
}

func nonEmptyPhase(t *testing.T, _ Config, body map[string]any) {
	t.Helper()
	if phase, _ := body["phase"].(string); phase == "" {
		t.Fatalf("the verb answered no phase: %v", body)
	}
}

func writeInsecureKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.kubeconfig")
	doc := `apiVersion: v1
kind: Config
clusters:
- name: kcp
  cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
users:
- name: gateway
  user:
    token: test-only-not-a-credential
contexts:
- name: kcp
  context:
    cluster: kcp
    user: gateway
current-context: kcp
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	return path
}

func signToken(t *testing.T, priv atcrypto.PrivateKey, aud, nsid string) string {
	t.Helper()
	iss, err := syntax.ParseDID(answeringCaller)
	if err != nil {
		t.Fatalf("ParseDID: %v", err)
	}
	lex, err := syntax.ParseNSID(nsid)
	if err != nil {
		t.Fatalf("ParseNSID(%s): %v", nsid, err)
	}
	tok, err := auth.SignServiceAuth(iss, aud, atprotoauth.MaxServiceAuthLifetime, &lex, priv)
	if err != nil {
		t.Fatalf("SignServiceAuth: %v", err)
	}
	return tok
}

type fakeKCP struct {
	mu      sync.Mutex
	objects map[string]map[string]any
	seen_   []string
}

func newFakeKCP(did, cluster string) *fakeKCP {
	f := &fakeKCP{objects: map[string]map[string]any{}}
	f.put("workspaces", kcpclient.AccountHash(did), map[string]any{
		"kind": "Workspace", "apiVersion": "tenancy.kcp.io/v1alpha1",
		"metadata": map[string]any{"name": kcpclient.AccountHash(did)},
		"spec":     map[string]any{},
	})
	f.put("workspaces", cluster, map[string]any{
		"kind": "Workspace", "apiVersion": "tenancy.kcp.io/v1alpha1",
		"metadata": map[string]any{"name": cluster},
		"spec":     map[string]any{},
	})
	f.put("workspaces", kcpclient.ChildLabel, map[string]any{
		"kind": "Workspace", "apiVersion": "tenancy.kcp.io/v1alpha1",
		"metadata": map[string]any{"name": kcpclient.ChildLabel},
		"spec": map[string]any{"mount": map[string]any{"ref": map[string]any{
			"apiVersion": "socialweb.computer/v1alpha1", "kind": "Cluster", "name": cluster,
		}}},
	})
	f.put("atprotoidentities", kcpclient.IdentityRecordName(did), map[string]any{
		"kind": "AtprotoIdentity", "apiVersion": "socialweb.computer/v1alpha1",
		"metadata": map[string]any{"name": kcpclient.IdentityRecordName(did)},
		"spec":     map[string]any{"did": did, "scope": "Account", "revoked": false},
	})
	f.put("clusters", cluster, map[string]any{
		"kind": "Cluster", "apiVersion": "socialweb.computer/v1alpha1",
		"metadata": map[string]any{"name": cluster},
		"spec":     map[string]any{"account": did, "running": false},
		"status":   map[string]any{"phase": "Ready", "URL": "", "instance": "1"},
	})
	return f
}

func (f *fakeKCP) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen_...)
}

func (f *fakeKCP) get(resource, name string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[resource+"\x00"+name]
	if !ok {
		return nil, false
	}
	out := make(map[string]any, len(obj)+1)
	for k, v := range obj {
		out[k] = v
	}
	return out, true
}

func (f *fakeKCP) put(resource, name string, obj map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[resource+"\x00"+name] = obj
}

func (f *fakeKCP) del(resource, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, resource+"\x00"+name)
}

func (f *fakeKCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	resource, name := kcpResource(r.URL.Path)

	f.mu.Lock()
	f.seen_ = append(f.seen_, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		if name != "" {
			obj, ok := f.get(resource, name)
			if !ok {
				writeKCPStatus(w, http.StatusNotFound, "NotFound")
				return
			}
			if resource == "workspaces" {
				obj["status"] = map[string]any{
					"phase": string(corev1alpha1.LogicalClusterPhaseReady),
				}
			}
			writeKCPObject(w, http.StatusOK, obj)
			return
		}
		writeKCPObject(w, http.StatusOK, f.list(resource))
	case http.MethodPost:
		var obj map[string]any
		if err := json.Unmarshal(body, &obj); err != nil {
			writeKCPStatus(w, http.StatusBadRequest, "BadRequest")
			return
		}
		created, _ := obj["metadata"].(map[string]any)["name"].(string)
		if created == "" {
			writeKCPStatus(w, http.StatusBadRequest, "BadRequest")
			return
		}
		if _, dup := f.get(resource, created); dup {
			writeKCPStatus(w, http.StatusConflict, "AlreadyExists")
			return
		}
		f.put(resource, created, obj)
		writeKCPObject(w, http.StatusCreated, obj)
	case http.MethodPatch:
		obj, ok := f.get(resource, name)
		if !ok {
			writeKCPStatus(w, http.StatusNotFound, "NotFound")
			return
		}
		var patch map[string]any
		if err := json.Unmarshal(body, &patch); err != nil {
			writeKCPStatus(w, http.StatusBadRequest, "BadRequest")
			return
		}
		mergeKCP(obj, patch)
		f.put(resource, name, obj)
		writeKCPObject(w, http.StatusOK, obj)
	case http.MethodDelete:
		f.del(resource, name)
		writeKCPObject(w, http.StatusOK, map[string]any{
			"kind": "Status", "apiVersion": "v1", "status": "Success", "code": http.StatusOK,
		})
	default:
		writeKCPStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (f *fakeKCP) list(resource string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind, apiVersion := kcpListKind(resource)
	items := []any{}
	names := make([]string, 0, len(f.objects))
	for k := range f.objects {
		if strings.HasPrefix(k, resource+"\x00") {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		items = append(items, f.objects[k])
	}
	return map[string]any{"kind": kind, "apiVersion": apiVersion, "items": items}
}

func kcpListKind(resource string) (string, string) {
	switch resource {
	case "workspaces":
		return "WorkspaceList", "tenancy.kcp.io/v1alpha1"
	case "clusters":
		return "ClusterList", "socialweb.computer/v1alpha1"
	case "nodes":
		return "NodeList", "socialweb.computer/v1alpha1"
	case "atprotoidentities":
		return "AtprotoIdentityList", "socialweb.computer/v1alpha1"
	case "configmaps":
		return "ConfigMapList", "v1"
	case "serviceaccounts":
		return "ServiceAccountList", "v1"
	default:
		return "List", "v1"
	}
}

func kcpResource(path string) (resource, name string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	last := parts[len(parts)-1]
	switch last {
	case "workspaces", "atprotoidentities", "clusters", "nodes", "configmaps", "serviceaccounts":
		return last, ""
	default:
		if len(parts) < 2 {
			return last, ""
		}
		return parts[len(parts)-2], last
	}
}

func mergeKCP(into, patch map[string]any) {
	for k, v := range patch {
		nested, ok := v.(map[string]any)
		if !ok {
			into[k] = v
			continue
		}
		existing, ok := into[k].(map[string]any)
		if !ok {
			existing = map[string]any{}
			into[k] = existing
		}
		mergeKCP(existing, nested)
	}
}

func writeKCPObject(w http.ResponseWriter, status int, obj map[string]any) {
	encoded, err := json.Marshal(obj)
	if err != nil {
		writeKCPStatus(w, http.StatusInternalServerError, "InternalError")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(encoded)
}

func writeKCPStatus(w http.ResponseWriter, status int, reason string) {
	writeKCPObject(w, status, map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "code": status, "message": fmt.Sprintf("fake kcp: %s", reason),
	})
}
