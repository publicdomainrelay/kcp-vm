package devicekey

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kubeconfig"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantclient"
)

const (
	kcpHost    = "https://kcp.test:6443"
	tenantHost = "https://tenant.test:16443"

	gatewayDID  = "did:web:gateway.test#socialweb"
	registryWS  = "root"
	registryNS  = "default"
	testDID     = "did:plc:testaccount0000000000000000"
	testCluster = "web"
	issuedToken = "sa-token-from-tokenrequest"

	execCommand = "/usr/local/bin/socialweb-credential"
	execEnvName = "SOCIALWEB_DEVICE_KEY"
)

func expiresIn() string { return time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }

type fakeApiservers struct {
	mu   sync.Mutex
	rows map[string]tenantclient.Row
	reqs []string

	saExists  bool
	reachable bool
}

func newFake() *fakeApiservers {
	return &fakeApiservers{rows: map[string]tenantclient.Row{}, saExists: true, reachable: true}
}

func (f *fakeApiservers) RoundTrip(r *http.Request) (*http.Response, error) {
	path := r.URL.Path
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+path)
	f.mu.Unlock()

	tenant := r.URL.Host == "tenant.test:16443"
	if tenant && !f.reachable {
		return nil, errors.New("fakeApiservers: the tenant apiserver is not answering")
	}

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/configmaps"):
		var cm corev1.ConfigMap
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &cm); err != nil {
			return f.status(http.StatusBadRequest, "BadRequest")
		}
		f.mu.Lock()
		f.rows[cm.Name] = tenantclient.Row{
			DeviceKey: cm.Data["deviceKey"], DID: cm.Data["did"], Cluster: cm.Data["cluster"],
		}
		f.mu.Unlock()
		return f.respond(http.StatusCreated, cm)

	case r.Method == http.MethodGet && strings.Contains(path, "/configmaps/"):
		name := path[strings.LastIndex(path, "/")+1:]
		f.mu.Lock()
		row, ok := f.rows[name]
		f.mu.Unlock()
		if !ok {
			return f.status(http.StatusNotFound, "NotFound")
		}
		return f.respond(http.StatusOK, corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: registryNS},
			Data: map[string]string{
				"deviceKey": row.DeviceKey, "did": row.DID, "cluster": row.Cluster,
			},
		})

	case r.Method == http.MethodGet && strings.Contains(path, "/serviceaccounts/"):
		f.mu.Lock()
		exists := f.saExists
		f.mu.Unlock()
		if !exists {
			return f.status(http.StatusNotFound, "NotFound")
		}
		name := path[strings.LastIndex(path, "/")+1:]
		return f.respond(http.StatusOK, corev1.ServiceAccount{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenantclient.Namespace},
		})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/token"):
		raw, _ := json.Marshal(map[string]any{
			"kind": "TokenRequest", "apiVersion": "authentication.k8s.io/v1",
			"status": map[string]any{"token": issuedToken, "expirationTimestamp": expiresIn()},
		})
		return f.respond(http.StatusCreated, json.RawMessage(raw))

	case r.Method == http.MethodGet && strings.Contains(path, "/clusters/"):
		name := path[strings.LastIndex(path, "/")+1:]
		cl := v1alpha1.Cluster{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Cluster"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       v1alpha1.ClusterSpec{Account: testDID, Running: true},
		}
		cl.Status.Instance = "instance-1"
		return f.respond(http.StatusOK, cl)
	}

	return f.status(http.StatusNotFound, "NotFound")
}

func (f *fakeApiservers) respond(code int, obj any) (*http.Response, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(raw))),
	}, nil
}

func (f *fakeApiservers) status(code int, reason string) (*http.Response, error) {
	raw, _ := json.Marshal(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure, Code: int32(code),
		Reason: metav1.StatusReason(reason), Message: reason,
	})
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(raw))),
	}, nil
}

func (f *fakeApiservers) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func tenantStore(t *testing.T, f *fakeApiservers) *tenantclient.Client {
	t.Helper()
	c, err := tenantclient.New(tenantclient.Options{
		Registry:          &rest.Config{Host: kcpHost, Transport: f},
		RegistryWorkspace: registryWS,
		RegistryNamespace: registryNS,
		Audience:          gatewayDID,
		MintConfig: func(cluster string) (*rest.Config, error) {
			if cluster != testCluster {
				return nil, fmt.Errorf("no mint credential for %q", cluster)
			}
			return &rest.Config{Host: tenantHost, Transport: f}, nil
		},
	})
	if err != nil {
		t.Fatalf("tenantclient.New: %v", err)
	}
	return c
}

func granter(t *testing.T, b Binder) *Granter {
	t.Helper()
	g, err := New(Options{
		Binder:      b,
		Audience:    gatewayDID,
		TTL:         365 * 24 * time.Hour,
		ExecCommand: execCommand,
		ExecEnvName: execEnvName,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func grantInput() kcpclient.GrantInput {
	return kcpclient.GrantInput{
		Caller:  "did:plc:testaccount0000000000000000",
		Cluster: testCluster,
		Server:  kcpHost + "/clusters/root:abc:" + testCluster + ":clusters",
	}
}

func execEnvValue(t *testing.T, rendered string) string {
	t.Helper()
	var doc struct {
		Users []struct {
			User struct {
				Exec *struct {
					Env []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"exec"`
			} `json:"user"`
		} `json:"users"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &doc); err != nil {
		t.Fatalf("the rendered kubeconfig does not parse: %v\n%s", err, rendered)
	}
	if len(doc.Users) != 1 || doc.Users[0].User.Exec == nil {
		t.Fatalf("the rendered kubeconfig carries no exec block:\n%s", rendered)
	}
	for _, e := range doc.Users[0].User.Exec.Env {
		if e.Name == execEnvName {
			return e.Value
		}
	}
	t.Fatalf("the exec block carries no %s:\n%s", execEnvName, rendered)
	return ""
}

type recordingBinder struct {
	mu    sync.Mutex
	rows  []tenantclient.Row
	calls int
	err   error
}

func (b *recordingBinder) Record(_ context.Context, r tenantclient.Row) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return b.err
	}
	b.rows = append(b.rows, r)
	return nil
}

func TestABinderThatFailsHandsOutNothing(t *testing.T) {
	b := &recordingBinder{err: errors.New("kcp refused the ConfigMap")}
	g := granter(t, b)

	out, err := g.Grant(context.Background(), grantInput())
	if err == nil {
		t.Fatal("a grant whose binding was not written succeeded")
	}
	if out != "" {
		t.Fatalf("a grant whose binding was not written still produced a kubeconfig:\n%s\n"+
			"every credential in it resolves to nothing, so this is a user holding a file "+
			"that fails as an authentication error forever", out)
	}
	if !strings.Contains(err.Error(), "kcp refused the ConfigMap") {
		t.Fatalf("the refusal does not carry the binder's reason, so an operator cannot tell "+
			"a refused write from a refused key: %v", err)
	}
	if b.calls != 1 {
		t.Fatalf("the binder ran %d time(s)", b.calls)
	}
}

func TestTheRowIsTheVerifiedCallerAndTheCluster(t *testing.T) {
	b := &recordingBinder{}
	g := granter(t, b)

	in := grantInput()
	if _, err := g.Grant(context.Background(), in); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if len(b.rows) != 1 {
		t.Fatalf("the grant wrote %d row(s)", len(b.rows))
	}
	row := b.rows[0]
	if row.DID != string(in.Caller) {
		t.Fatalf("the row binds the key to %q, but the grant was for the VERIFIED caller %q -- "+
			"the row is the authority for (did, cluster) and Resolve trusts nothing else",
			row.DID, in.Caller)
	}
	if row.Cluster != in.Cluster {
		t.Fatalf("the row names cluster %q, want %q", row.Cluster, in.Cluster)
	}
	if !strings.HasPrefix(row.DeviceKey, "did:key:z") {
		t.Fatalf("the row's key is %q, which is not a did:key: the mint resolves a did:key "+
			"locally and NOTHING else, so a non-did:key here is a credential that can never "+
			"be verified", row.DeviceKey)
	}
}

func TestTheRowNameIsTheKeyTheMintLooksUp(t *testing.T) {
	b := &recordingBinder{}
	g := granter(t, b)
	if _, err := g.Grant(context.Background(), grantInput()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	name := tenantclient.RowName(b.rows[0].DeviceKey)
	if strings.ContainsAny(name, ":./_") || strings.ToLower(name) != name {
		t.Fatalf("row name %q is not a DNS-1123 label", name)
	}
}

func TestWhatTheKubeconfigCarriesIsWhatTheMintResolves(t *testing.T) {
	f := newFake()
	g := granter(t, tenantStore(t, f))
	in := grantInput()

	rendered, err := g.Grant(context.Background(), in)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	credential := execEnvValue(t, rendered)

	rowName := tenantclient.RowName(issuerOf(t, credential))
	if _, ok := f.rows[rowName]; !ok {
		t.Fatalf("the kubeconfig carries a credential whose key has no row: %s is absent from "+
			"the registry (%v)", rowName, f.calls())
	}

	m := mint.New(tenantStore(t, f), mint.Options{})
	cred, err := m.Issue(context.Background(), mint.DeviceCredential{Token: credential}, time.Now())
	if err != nil {
		t.Fatalf("the mint refused the credential getKubeconfig just handed out: %v\n"+
			"this is the hole Record had no caller for: EVERY credential answered "+
			"ErrBindingUnknown, so the whole renewal path was dead", err)
	}
	if cred.Status.Token != issuedToken {
		t.Fatalf("the mint returned token %q, want the apiserver's %q", cred.Status.Token, issuedToken)
	}

	if cred.Status.ExpirationTimestamp == "" {
		t.Fatal("the minted credential carries no expirationTimestamp")
	}
}

func TestTheMintFailsClosedOnACredentialThatWasNotBound(t *testing.T) {
	f := newFake()
	real := tenantStore(t, f)

	bound, err := granter(t, real).Grant(context.Background(), grantInput())
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}

	unbound, err := granter(t, forgetful{}).Grant(context.Background(), grantInput())
	if err != nil {
		t.Fatalf("Grant with a binder that records nothing: %v", err)
	}

	boundKey, unboundKey := issuerOf(t, execEnvValue(t, bound)), issuerOf(t, execEnvValue(t, unbound))
	if boundKey == unboundKey {
		t.Fatalf("both kubeconfigs carry the same device key %s", boundKey)
	}

	m := mint.New(real, mint.Options{})
	if _, err := m.Issue(context.Background(), mint.DeviceCredential{Token: execEnvValue(t, bound)}, time.Now()); err != nil {
		t.Fatalf("the BOUND credential was refused: %v", err)
	}
	_, err = m.Issue(context.Background(), mint.DeviceCredential{Token: execEnvValue(t, unbound)}, time.Now())
	if !errors.Is(err, mint.ErrBindingUnknown) {
		t.Fatalf("the mint answered %v for a credential bound to nothing; it must fail closed "+
			"with ErrBindingUnknown -- anything else and a key that was never granted is "+
			"either usable or indistinguishable from a kcp outage", err)
	}
}

type forgetful struct{}

func (forgetful) Record(context.Context, tenantclient.Row) error { return nil }

func issuerOf(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the device credential is not a JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("the credential's payload is not base64url: %v", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Aud string `json:"aud"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("the credential's payload is not JSON: %v (%s)", err, raw)
	}
	if claims.Aud != gatewayDID {
		t.Fatalf("the credential is signed for audience %q, but the mint checks %q -- an "+
			"audience the mint does not check is a credential refused at the endpoint it was "+
			"minted for", claims.Aud, gatewayDID)
	}
	return claims.Iss
}

func TestNewRefusesAnUnusableConfiguration(t *testing.T) {
	complete := func() Options {
		return Options{
			Binder:      &recordingBinder{},
			Audience:    gatewayDID,
			TTL:         time.Hour,
			ExecCommand: execCommand,
			ExecEnvName: execEnvName,
		}
	}
	for _, tc := range []struct {
		name   string
		break_ func(*Options)
		want   string
	}{
		{"no binder", func(o *Options) { o.Binder = nil }, "Binder"},
		{"no audience", func(o *Options) { o.Audience = "" }, "Audience"},
		{"no ttl", func(o *Options) { o.TTL = 0 }, "TTL"},
		{"negative ttl", func(o *Options) { o.TTL = -time.Second }, "TTL"},
		{"no exec command", func(o *Options) { o.ExecCommand = "" }, "ExecCommand"},
		{"no env name", func(o *Options) { o.ExecEnvName = "" }, "ExecEnvName"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := complete()
			tc.break_(&o)
			if _, err := New(o); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not name %q: %v", tc.want, err)
			}
		})
	}
}

func TestGrantRefusesAnIncompleteInput(t *testing.T) {
	g := granter(t, &recordingBinder{})
	for _, tc := range []struct {
		name string
		in   kcpclient.GrantInput
	}{
		{"no caller", kcpclient.GrantInput{Cluster: testCluster, Server: "https://kcp.test/clusters/x"}},
		{"no cluster", kcpclient.GrantInput{Caller: testDID, Server: "https://kcp.test/clusters/x"}},
		{"no server", kcpclient.GrantInput{Caller: testDID, Cluster: testCluster}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := g.Grant(context.Background(), tc.in); err == nil {
				t.Fatalf("%s was accepted: %q", tc.name, out)
			}
		})
	}
}

func TestTheRenderedKubeconfigIsOneAUserMayHold(t *testing.T) {
	f := newFake()
	rendered, err := granter(t, tenantStore(t, f)).Grant(context.Background(), grantInput())
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if !strings.Contains(rendered, "https://") {
		t.Fatalf("the kubeconfig's server is not https, so client-go will silently drop every "+
			"credential in it:\n%s", rendered)
	}
	if err := kubeconfig.AssertClean(rendered); err != nil {
		t.Fatalf("the rendered document is not one a user may hold: %v", err)
	}
	if strings.Contains(rendered, "PRIVATE KEY") {
		t.Fatalf("the kubeconfig carries a private key rather than the signed credential:\n%s", rendered)
	}
}

func TestTheGeneratedKeyIsOneTheMintCanResolve(t *testing.T) {
	b := &recordingBinder{}
	if _, err := granter(t, b).Grant(context.Background(), grantInput()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err := atcrypto.ParsePublicDIDKey(b.rows[0].DeviceKey); err != nil {
		t.Fatalf("the bound key does not parse as a did:key: %v", err)
	}
}
