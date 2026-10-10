package tenantclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
)

const (
	tenantHost  = "https://tenant.test:16443"
	kcpHost     = "https://kcp.test:6443"
	gatewayDID  = "did:web:gateway.test#socialweb"
	registryWS  = "root"
	registryNS  = "default"
	testDID     = "did:plc:testaccount0000000000000000"
	testCluster = "web"
	saName      = "did-mruwiotqnrrtu5dfon2gcy3dn52w45bqgaydambqgaydambqgaydambq"
	tokenExpiry = "2026-09-22T12:15:00Z"
	issuedToken = "sa-token-from-tokenrequest"
	tenantPath  = "/api/v1/namespaces/" + Namespace + "/serviceaccounts/"
)

type request struct {
	Method string
	Path   string
	Body   string
}

func (r request) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		t.Fatalf("%s %s: body is not JSON: %v (%s)", r.Method, r.Path, err, r.Body)
	}
	return out
}

func (r request) isWrite() bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

type harness struct {
	mu       sync.Mutex
	requests []request

	row           Row
	saExists      bool
	clusterExists bool
	instance      string
	running       bool

	token  string
	expiry string
}

func (h *harness) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	r := request{Method: req.Method, Path: req.URL.Path, Body: body}
	h.mu.Lock()
	h.requests = append(h.requests, r)
	h.mu.Unlock()

	status, payload := h.answer(r)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
		Request:    req,
	}, nil
}

func (h *harness) answer(r request) (int, string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	rowCollection := "/clusters/" + registryWS + "/api/v1/namespaces/" + registryNS + "/configmaps"
	rowPath := rowCollection + "/"
	clusterPath := "/apis/" + v1alpha1.GroupName + "/" + v1alpha1.Version + "/clusters/"

	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.Path, rowPath):
		if h.row.DeviceKey == "" || r.Path != rowPath+RowName(h.row.DeviceKey) {
			return notFound("configmaps", strings.TrimPrefix(r.Path, rowPath))
		}
		cm := corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: metav1.ObjectMeta{Name: RowName(h.row.DeviceKey), Namespace: registryNS},
			Data: map[string]string{
				"deviceKey": h.row.DeviceKey, "did": h.row.DID, "cluster": h.row.Cluster,
			},
		}
		return encode(cm)

	case r.Method == http.MethodPost && r.Path == rowCollection:
		var cm corev1.ConfigMap
		if err := json.Unmarshal([]byte(r.Body), &cm); err != nil {
			return http.StatusBadRequest, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":400}`
		}
		h.row = Row{DeviceKey: cm.Data["deviceKey"], DID: cm.Data["did"], Cluster: cm.Data["cluster"]}
		return encode(cm)

	case r.Method == http.MethodGet && strings.Contains(r.Path, clusterPath):
		if !h.clusterExists {
			return notFound("clusters", strings.TrimPrefix(r.Path, clusterPath))
		}
		cl := v1alpha1.Cluster{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Cluster"},
			ObjectMeta: metav1.ObjectMeta{Name: strings.TrimPrefix(r.Path, clusterPath)},
			Spec:       v1alpha1.ClusterSpec{Account: testDID, Running: h.running},
		}
		cl.Status.Instance = h.instance
		return encode(cl)

	case r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/token"):
		status := map[string]any{"token": h.token}
		if h.expiry != "" {
			status["expirationTimestamp"] = h.expiry
		}
		out := map[string]any{
			"kind":       "TokenRequest",
			"apiVersion": "authentication.k8s.io/v1",
			"metadata":   map[string]any{"name": saName, "namespace": Namespace},
			"status":     status,
		}
		raw, _ := json.Marshal(out)
		return http.StatusCreated, string(raw)

	case r.Method == http.MethodGet && strings.HasPrefix(r.Path, tenantPath):
		if !h.saExists {
			return notFound("serviceaccounts", strings.TrimPrefix(r.Path, tenantPath))
		}
		sa := corev1.ServiceAccount{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
			ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: Namespace},
		}
		return encode(sa)
	}

	return http.StatusNotFound, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":404,` +
		`"reason":"NotFound","message":"the harness has no answer for ` + r.Method + " " + r.Path + `"}`
}

func (h *harness) last(t *testing.T) request {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) == 0 {
		t.Fatal("no request was made")
	}
	return h.requests[len(h.requests)-1]
}

func (h *harness) count(method, pathFragment string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.requests {
		if r.Method == method && strings.Contains(r.Path, pathFragment) {
			n++
		}
	}
	return n
}

func (h *harness) writes(t *testing.T) []request {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []request
	for _, r := range h.requests {
		if r.isWrite() {
			out = append(out, r)
		}
	}
	return out
}

func encode(obj any) (int, string) {
	raw, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return http.StatusOK, string(raw)
}

func notFound(resource, name string) (int, string) {
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Code:     http.StatusNotFound,
		Reason:   metav1.StatusReasonNotFound,
		Message:  fmt.Sprintf("%s %q not found", resource, name),
	}
	raw, _ := json.Marshal(status)
	return http.StatusNotFound, string(raw)
}

func newHarness(t *testing.T) (*harness, *Client, mint.DeviceCredential, string) {
	t.Helper()
	h := &harness{
		saExists:      true,
		clusterExists: true,
		instance:      "instance-1",
		running:       true,
		token:         issuedToken,
		expiry:        tokenExpiry,
	}
	c, err := New(Options{
		Registry:          &rest.Config{Host: kcpHost, Transport: h},
		RegistryWorkspace: registryWS,
		RegistryNamespace: registryNS,
		Audience:          gatewayDID,
		MintConfig: func(did, cluster string) (*rest.Config, error) {
			_ = did
			if cluster != testCluster {
				return nil, fmt.Errorf("no mint credential for %q", cluster)
			}
			return &rest.Config{Host: tenantHost, Transport: h}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cred, deviceKey := deviceCredential(t)
	h.row = Row{DeviceKey: deviceKey, DID: testDID, Cluster: testCluster}
	return h, c, cred, deviceKey
}

func deviceCredential(t *testing.T) (mint.DeviceCredential, string) {
	t.Helper()
	priv, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	deviceKey := pub.DIDKey()
	if !strings.HasPrefix(deviceKey, "did:key:z") {
		t.Fatalf("the device key is not a did:key: %q", deviceKey)
	}
	signed, err := auth.SignServiceAuth(syntax.DID(deviceKey), gatewayDID, time.Hour, nil, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return mint.DeviceCredential{Token: signed}, deviceKey
}

func now() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }

func TestRecordThenResolveClosesTheLoop(t *testing.T) {
	h, c, cred, deviceKey := newHarness(t)
	h.row = Row{}

	if err := c.Record(context.Background(), Row{DeviceKey: deviceKey, DID: testDID, Cluster: testCluster}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	b, err := c.Resolve(context.Background(), cred)
	if err != nil {
		t.Fatalf("Resolve after Record: %v", err)
	}
	if b.DID != testDID || b.Cluster != testCluster || b.SA != SAName(testDID) {
		t.Fatalf("binding = %+v", b)
	}
	if w := h.writes(t); len(w) != 1 || !strings.HasSuffix(w[0].Path, "/configmaps") {
		t.Fatalf("writes = %+v", w)
	}
	if err := c.Record(context.Background(), Row{DeviceKey: deviceKey}); err == nil {
		t.Fatal("a row with no DID or cluster was written")
	}
}

func TestThePinnedNames(t *testing.T) {
	if got, want := SAName("did:web:example.com"), "did-mruwiotxmvrduzlymfwxa3dffzrw63i"; got != want {
		t.Fatalf("SAName(did:web:example.com) = %q, want %q", got, want)
	}
	if SAName(testDID) != saName {
		t.Fatalf("SAName(%q) = %q, but the harness and the grant use %q", testDID, SAName(testDID), saName)
	}
	for _, name := range []string{SAName("did:web:example.com"), RowName("did:key:zQ3shokFTS3brHcDQrn82RUDfCZESWL1ZdCEJwekUDPQiYBme")} {
		if strings.ContainsAny(name, ":./_") || strings.ToLower(name) != name {
			t.Fatalf("%q is not a DNS-1123 label: base32 emits only [a-z2-7], and no path segment "+
				"may carry a colon", name)
		}
	}
}

func TestResolveRefusesAnUnknownCredentialAndCreatesNothing(t *testing.T) {
	h, c, cred, _ := newHarness(t)
	h.row = Row{}

	if _, err := c.Resolve(context.Background(), cred); !errors.Is(err, mint.ErrBindingUnknown) {
		t.Fatalf("want ErrBindingUnknown, got %v", err)
	}
	if w := h.writes(t); len(w) != 0 {
		t.Fatalf("Resolve wrote %d object(s): %+v -- the mint must never create anything", len(w), w)
	}
}

func TestARowUnderAnotherKeyDoesNotBind(t *testing.T) {
	h, c, cred, _ := newHarness(t)
	_, stranger := deviceCredential(t)
	h.row.DeviceKey = stranger

	if _, err := c.Resolve(context.Background(), cred); !errors.Is(err, mint.ErrBindingUnknown) {
		t.Fatalf("want ErrBindingUnknown when the registry holds no row under the verified key, got %v", err)
	}
}

func TestResolveReportsTheServiceAccountGone(t *testing.T) {
	h, c, cred, _ := newHarness(t)

	b, err := c.Resolve(context.Background(), cred)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !b.SAExists || b.SA != saName || b.DID != testDID || b.Cluster != testCluster || b.Instance != "instance-1" {
		t.Fatalf("binding = %+v", b)
	}

	h.saExists = false
	b, err = c.Resolve(context.Background(), cred)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if b.SAExists {
		t.Fatal("a DELETED ServiceAccount still reported as existing: revocation would not take " +
			"effect on the path a runner actually uses, which runs every fifteen minutes")
	}
	if n := h.count(http.MethodGet, tenantPath); n != 2 {
		t.Fatalf("the ServiceAccount was read %d time(s) across two Resolves; it must be read on "+
			"EVERY call and cached nowhere -- an answer remembered is a revoked identity that keeps "+
			"minting", n)
	}
}

func TestTheMintFailsClosedOnADeletedServiceAccountWithNoTokenIssued(t *testing.T) {
	h, c, cred, _ := newHarness(t)
	h.saExists = false

	_, err := mint.New(c, mint.Options{}).Issue(context.Background(), cred, now())
	if !errors.Is(err, mint.ErrServiceAccountGone) {
		t.Fatalf("want ErrServiceAccountGone, got %v", err)
	}
	if n := h.count(http.MethodPost, "/token"); n != 0 {
		t.Fatalf("a token was requested %d time(s) for a DELETED ServiceAccount", n)
	}
}

func TestIssueAsksForNoAudience(t *testing.T) {
	h, c, _, _ := newHarness(t)

	if _, _, err := c.Issue(context.Background(), binding(), 15*time.Minute); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sent := h.last(t)
	body := sent.json(t)

	spec, ok := body["spec"].(map[string]any)
	if !ok {
		t.Fatalf("no spec in the TokenRequest: %v", body)
	}
	auds, present := spec["audiences"]
	if !present {
		t.Fatalf("audiences is ABSENT: the apiserver would substitute --api-audiences, but the "+
			"ADR measured the empty list and this request must say so: %v", spec)
	}
	list, ok := auds.([]any)
	if !ok {
		t.Fatalf("audiences is not a list: %#v", auds)
	}
	if len(list) != 0 {
		t.Fatalf("audiences = %v; a custom audience is issued and then 401s through the mount, so "+
			"a token can be minted that can never be used", list)
	}
	if strings.Contains(sent.Body, "gateway.test") {
		t.Fatalf("the gateway's own service DID reached the TokenRequest as an audience: %s", sent.Body)
	}
}

func TestTheTokenRequestCarriesTheTTLAndNoBinding(t *testing.T) {
	h, c, _, _ := newHarness(t)
	if _, _, err := c.Issue(context.Background(), binding(), 15*time.Minute); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sent := h.last(t)
	spec, _ := sent.json(t)["spec"].(map[string]any)

	if got := spec["expirationSeconds"]; got != float64(900) {
		t.Fatalf("expirationSeconds = %v, want 900 (the 15-minute TTL in seconds)", got)
	}
	if got, present := spec["boundObjectRef"]; present && got != nil {
		t.Fatalf("boundObjectRef = %v; the default is null and binding buys nothing we want", got)
	}
	if want := tenantPath + saName + "/token"; sent.Path != want {
		t.Fatalf("TokenRequest path = %q, want %q", sent.Path, want)
	}
}

func TestIssueRefusesATTLBelowTheFloor(t *testing.T) {
	h, c, _, _ := newHarness(t)

	if _, _, err := c.Issue(context.Background(), binding(), MinTokenTTL-time.Second); !errors.Is(err, ErrTTLBelowFloor) {
		t.Fatalf("want ErrTTLBelowFloor for %s, got %v", MinTokenTTL-time.Second, err)
	}
	if n := h.count(http.MethodPost, "/token"); n != 0 {
		t.Fatalf("a below-floor TokenRequest was sent to the apiserver %d time(s)", n)
	}
	if _, _, err := c.Issue(context.Background(), binding(), MinTokenTTL); err != nil {
		t.Fatalf("Issue at the floor: %v", err)
	}
}

func TestIssueReturnsTheTokensOwnExpiry(t *testing.T) {
	_, c, _, _ := newHarness(t)

	token, expires, err := c.Issue(context.Background(), binding(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token != issuedToken {
		t.Fatalf("token = %q", token)
	}
	want, _ := time.Parse(time.RFC3339, tokenExpiry)
	if !expires.Equal(want) {
		t.Fatalf("expires = %s, want the apiserver's %s -- a locally computed expiry is a guess "+
			"about a token whose real one the apiserver already told us", expires, want)
	}
}

func TestAnAnswerWithoutARealExpiryIsRefused(t *testing.T) {
	h, c, _, _ := newHarness(t)
	h.expiry = ""

	if _, _, err := c.Issue(context.Background(), binding(), 15*time.Minute); !errors.Is(err, ErrNoExpiration) {
		t.Fatalf("want ErrNoExpiration, got %v", err)
	}
}

func TestAnUnresolvableClusterFailsClosed(t *testing.T) {
	h, c, cred, _ := newHarness(t)
	h.row.Cluster = "somewhere-else"

	if _, err := c.Resolve(context.Background(), cred); err == nil {
		t.Fatal("a row naming an unknown cluster resolved: the mint would have no credential to use")
	} else if errors.Is(err, mint.ErrBindingUnknown) {
		t.Fatalf("an unresolved cluster must be an ERROR, not a binding refusal: %v", err)
	}
	if n := h.count(http.MethodGet, tenantPath); n != 0 {
		t.Fatalf("a tenant apiserver was read %d time(s) for a cluster with no mint credential", n)
	}
}

func TestADeletedClusterFailsClosed(t *testing.T) {
	h, c, cred, _ := newHarness(t)
	h.clusterExists = false

	if _, err := c.Resolve(context.Background(), cred); !errors.Is(err, mint.ErrBindingUnknown) {
		t.Fatalf("want ErrBindingUnknown for a deleted Cluster, got %v", err)
	}
	if n := h.count(http.MethodGet, tenantPath); n != 0 {
		t.Fatalf("a tenant apiserver was read %d time(s) for a cluster that does not exist", n)
	}
}

func TestANonDeviceKeyIssuerIsRefused(t *testing.T) {
	dir := deviceKeysOnly{}
	for _, did := range []syntax.DID{"did:plc:testaccount0000000000000000", "did:web:example.com"} {
		if _, err := dir.LookupDID(context.Background(), did); err == nil {
			t.Fatalf("the device-key directory resolved %s; a stolen ATProto token must be "+
				"useless at the mint, and an uncached third-party fetch must stay off a path "+
				"that runs every fifteen minutes", did)
		}
	}
	_, deviceKey := deviceCredential(t)
	if _, err := dir.LookupDID(context.Background(), syntax.DID(deviceKey)); err != nil {
		t.Fatalf("the device-key directory refused a real did:key: %v", err)
	}
}

func TestTheGrantIsScopedByResourceNames(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/tenant-grant.yaml")
	if err != nil {
		t.Fatalf("read deploy/tenant-grant.yaml: %v", err)
	}
	rendered, err := kcpclient.RenderTenantGrant(string(raw), kcpclient.GrantScope{
		OwnerDID: grantExampleDID,
		ESOUser:  "system:socialweb:external-secrets",
	})
	if err != nil {
		t.Fatalf("the deployed grant is not renderable: %v", err)
	}
	docs := grantDocs([]byte(rendered))
	roleDoc, vapDoc := docOfKind(t, docs, "Role"), docOfKind(t, docs, "ValidatingAdmissionPolicy")

	var role struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Rules []struct {
			APIGroups     []string `json:"apiGroups"`
			Resources     []string `json:"resources"`
			Verbs         []string `json:"verbs"`
			ResourceNames []string `json:"resourceNames"`
		} `json:"rules"`
	}
	if err := yaml.Unmarshal(roleDoc, &role); err != nil {
		t.Fatalf("unmarshal the Role: %v", err)
	}
	if role.Kind != "Role" {
		t.Fatalf("the grant is a %s: a ClusterRole is namespace-wide, and stock `edit` grants the "+
			"very verb the ADR measured as total impersonation", role.Kind)
	}
	if role.Metadata.Namespace != Namespace || role.Metadata.Name != "gwmint" {
		t.Fatalf("grant is %s/%s, want %s/gwmint", role.Metadata.Namespace, role.Metadata.Name, Namespace)
	}

	var sawToken, sawRead bool
	for _, r := range role.Rules {
		if len(r.ResourceNames) == 0 {
			t.Fatalf("a rule with NO resourceNames: %+v -- `create serviceaccounts/token` without "+
				"resourceNames is TOTAL IMPERSONATION, which is the claim this ADR first made and "+
				"then measured to be false", r)
		}
		for _, name := range r.ResourceNames {
			if name == "*" || !strings.HasPrefix(name, saPrefix) {
				t.Fatalf("resourceName %q is not a derived ServiceAccount name (%s<base32>)", name, saPrefix)
			}
		}
		for _, list := range [][]string{r.Verbs, r.Resources, r.APIGroups} {
			for _, v := range list {
				if v == "*" {
					t.Fatalf("a wildcard in the mint grant: %+v", r)
				}
			}
		}
		if len(r.APIGroups) != 1 || r.APIGroups[0] != "" {
			t.Fatalf("the grant names apiGroups %v; serviceaccounts and their token subresource "+
				"are in the core group", r.APIGroups)
		}
		if len(r.Resources) != 1 {
			t.Fatalf("a rule names %v; each rule names ONE resource", r.Resources)
		}
		switch r.Resources[0] {
		case "serviceaccounts/token":
			if len(r.Verbs) != 1 || r.Verbs[0] != "create" {
				t.Fatalf("verbs on serviceaccounts/token = %v, want exactly [create]", r.Verbs)
			}
			sawToken = true
		case "serviceaccounts":
			if len(r.Verbs) != 1 || r.Verbs[0] != "get" {
				t.Fatalf("verbs on serviceaccounts = %v, want exactly [get]", r.Verbs)
			}
			sawRead = true
		default:
			t.Fatalf("the mint grant reaches %q; it holds two rules and no more", r.Resources)
		}
	}
	if !sawToken || !sawRead {
		t.Fatalf("the grant is missing a rule: token=%v read=%v", sawToken, sawRead)
	}

	if got, want := role.Rules[0].ResourceNames[0], SAName(grantExampleDID); got != want {
		t.Fatalf("the grant's first resourceName is %q but SAName(%q) is %q: the YAML and the "+
			"derivation have drifted, which either widens the grant or breaks every mint",
			got, grantExampleDID, want)
	}

	var vap struct {
		Kind string `json:"kind"`
		Spec struct {
			FailurePolicy string `json:"failurePolicy"`
			Match         struct {
				Rules []struct {
					Resources  []string `json:"resources"`
					Operations []string `json:"operations"`
				} `json:"resourceRules"`
			} `json:"matchConstraints"`
			Validations []struct {
				Expression string `json:"expression"`
			} `json:"validations"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(vapDoc, &vap); err != nil {
		t.Fatalf("unmarshal the ValidatingAdmissionPolicy: %v", err)
	}
	if vap.Kind != "ValidatingAdmissionPolicy" {
		t.Fatalf("the VAP did not parse as one: kind=%q", vap.Kind)
	}
	if vap.Spec.FailurePolicy != "Fail" {
		t.Fatalf("failurePolicy = %q; a policy that fails OPEN when its expression cannot be "+
			"evaluated is a policy an operator can disable by accident", vap.Spec.FailurePolicy)
	}
	if len(vap.Spec.Validations) == 0 {
		t.Fatal("the VAP carries no validations")
	}
	body := ""
	for _, v := range vap.Spec.Validations {
		body += v.Expression + "\n"
	}
	for _, want := range []string{"gwmint", `did-[a-z2-7]+`, "request.namespace", "external-secrets"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the VAP's expressions do not mention %q:\n%s", want, body)
		}
	}
}

const grantExampleDID = "did:web:example.com"

func grantDocs(raw []byte) [][]byte {
	var out [][]byte
	for _, part := range strings.Split(string(raw), "\n---") {
		if strings.TrimSpace(part) != "" {
			out = append(out, []byte(part))
		}
	}
	return out
}

func docOfKind(t *testing.T, docs [][]byte, kind string) []byte {
	t.Helper()
	var found []byte
	type meta struct {
		Kind string `json:"kind"`
	}
	for _, doc := range docs {
		var m meta
		if err := yaml.Unmarshal(doc, &m); err != nil {
			t.Fatalf("unmarshal a grant document: %v", err)
		}
		if m.Kind == kind {
			if found != nil {
				t.Fatalf("the grant carries two %s documents", kind)
			}
			found = doc
		}
	}
	if found == nil {
		t.Fatalf("the grant carries no %s", kind)
	}
	return found
}

func binding() mint.Binding {
	return mint.Binding{
		DID: testDID, Cluster: testCluster, SA: saName, SAExists: true, Instance: "instance-1", Running: true,
	}
}
