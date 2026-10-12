package openbaoclient

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	openbao "github.com/openbao/openbao/api/v2"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/pki"
)

type request struct {
	Method string

	Path string

	Namespace string

	Token string

	Body string
}

func newServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*Client, *[]request) {
	t.Helper()
	seen := &[]request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*seen = append(*seen, request{
			Method:    r.Method,
			Path:      r.URL.Path,
			Namespace: r.Header.Get(openbao.NamespaceHeaderName),
			Token:     r.Header.Get("X-Vault-Token"),
			Body:      string(body),
		})
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := New(Options{Address: server.URL, Token: "vault-token"})
	if err != nil {
		t.Fatal(err)
	}
	return client, seen
}

func envelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func TestNewRequiresAnAddressAndCA(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("an address is required")
	}
	if _, err := New(Options{Address: "https://bao.invalid", CACert: []byte("not pem")}); !errors.Is(err, ErrNoCA) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestsCarryTheTokenAndNamespace(t *testing.T) {
	client, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sys/mounts" {
			envelope(w, map[string]any{})
			return
		}
		w.WriteHeader(204)
	})
	if err := client.EnsureMount(context.Background(), "alice.default", "pki", "pki"); err != nil {
		t.Fatal(err)
	}
	last := (*seen)[len(*seen)-1]
	if last.Token != "vault-token" {
		t.Fatalf("token header = %q", last.Token)
	}
	if last.Namespace != "alice.default" {
		t.Fatalf("namespace header = %q", last.Namespace)
	}
}

func TestEnsureMountReportsATypeMismatch(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		envelope(w, map[string]any{"pki/": map[string]any{"type": "kv"}})
	})
	err := client.EnsureMount(context.Background(), "alice.default", "pki", "pki")
	if err == nil || !strings.Contains(err.Error(), "mounted as") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureMountCreatesAMissingMount(t *testing.T) {
	client, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sys/mounts" {
			envelope(w, map[string]any{})
			return
		}
		w.WriteHeader(204)
	})
	if err := client.EnsureMount(context.Background(), "alice.default", "pki", "pki"); err != nil {
		t.Fatal(err)
	}
	last := (*seen)[len(*seen)-1]
	if last.Path != "/v1/sys/mounts/pki" || !strings.Contains(last.Body, `"type":"pki"`) {
		t.Fatalf("request = %+v", last)
	}
}

func TestNotFoundMapsToTheSentinel(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	})
	exists, err := client.NamespaceExists(context.Background(), "alice.default")
	if err != nil || exists {
		t.Fatalf("exists = (%v, %v)", exists, err)
	}
	if err := client.DeleteNamespace(context.Background(), "alice.default"); err != nil {
		t.Fatalf("deleting a missing namespace is not an error: %v", err)
	}
}

func TestForbiddenMapsToTheSentinel(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	})
	_, err := client.CASerial(context.Background(), "root", "pki")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	var response *ResponseError
	if !errors.As(err, &response) || response.Body != "permission denied" {
		t.Fatalf("the error body must be unwrapped from the envelope: %v", err)
	}
}

func TestGenerateRootReadsTheSerialFromTheCertificate(t *testing.T) {
	certificate := testCertificate(t)
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		envelope(w, map[string]any{"certificate": certificate})
	})
	root, err := client.GenerateRoot(context.Background(), "root", "pki", "kcp-mesh-root", "87600h")
	if err != nil {
		t.Fatal(err)
	}
	if root.Serial == "" {
		t.Fatal("the serial must be read out of the certificate")
	}
	if !strings.Contains(root.Serial, ":") {
		t.Fatalf("serial = %q, want colon-separated hexadecimal", root.Serial)
	}
}

func TestCAChainIsNotEnveloped(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n"))
	})
	chain, err := client.CAChain(context.Background(), "alice.default", "pki")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(chain, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("chain = %q", chain)
	}
}

func TestSignIntermediateConcatenatesTheChain(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		envelope(w, map[string]any{"certificate": "SIGNED", "ca_chain": []string{"ROOT"}})
	})
	chain, err := client.SignIntermediate(context.Background(), "root", "pki", "CSR", "alice.default.intermediate", "43800h")
	if err != nil {
		t.Fatal(err)
	}
	if chain != "SIGNED\nROOT" {
		t.Fatalf("chain = %q", chain)
	}
}

func TestIssueSeparatesIPsFromNames(t *testing.T) {
	client, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		envelope(w, map[string]any{
			"certificate":   "LEAF",
			"private_key":   "KEY",
			"ca_chain":      []string{"INTERMEDIATE"},
			"serial_number": "EE:FF",
		})
	})
	cert, err := client.Issue(context.Background(), "alice.default", "pki", "denopod", pki.CertRequest{
		CommonName: "pds.default.alice.svc.kcp.local",
		AltNames:   []string{"pds.default.alice.svc.kcp.local"},
		IPSANs:     []string{"127.0.0.1", "::1"},
		TTL:        "720h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cert.Certificate != "LEAF" || cert.PrivateKey != "KEY" || cert.Serial != "EE:FF" {
		t.Fatalf("cert = %+v", cert)
	}
	body := (*seen)[0].Body
	if !strings.Contains(body, `"alt_names"`) || !strings.Contains(body, `"ip_sans"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestWriteRoleCarriesDomainsAndKeyType(t *testing.T) {
	client, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	})
	err := client.WriteRole(context.Background(), "alice.default", "pki", "denopod", pki.Role{
		AllowedDomains:   []string{"kcp.local"},
		AllowSubdomains:  true,
		EnforceHostnames: true,
		KeyType:          "ec",
		KeyBits:          256,
		MaxTTL:           "720h",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := (*seen)[0].Body
	for _, want := range []string{`"allowed_domains":["kcp.local"]`, `"allow_subdomains":true`, `"key_type":"ec"`, `"key_bits":256`, `"allow_ip_sans":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s must contain %s", body, want)
		}
	}
}

func TestHealthAcceptsTheStandbyStatuses(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"standby":true,"version":"2.0.0"}`))
	})
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !health.Initialized || health.Sealed || health.Version != "2.0.0" {
		t.Fatalf("health = %+v", health)
	}
}

func TestTheEnvironmentCannotWeakenTheTLSConfiguration(t *testing.T) {
	t.Setenv("VAULT_SKIP_VERIFY", "1")
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"version":"2.0.0"}`))
	}))
	t.Cleanup(server.Close)
	trusted := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	good, err := New(Options{Address: server.URL, Token: "t", CACert: trusted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := good.Health(context.Background()); err != nil {
		t.Fatalf("the CA the caller supplied must be trusted: %v", err)
	}

	bad, err := New(Options{Address: server.URL, Token: "t", CACert: []byte(testCertificate(t))})
	if err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	if _, err := bad.Health(context.Background()); err == nil {
		t.Fatal("a CA that is not the server's must be rejected, whatever the environment says")
	}
	if hits.Load() != before {
		t.Fatal("the client reached the server despite an untrusted CA")
	}
}

func TestAgentAddressEnvironmentIsIgnored(t *testing.T) {
	t.Setenv("BAO_AGENT_ADDR", "https://127.0.0.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"version":"2.0.0"}`))
	}))
	t.Cleanup(server.Close)
	client, err := New(Options{Address: server.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("the address the caller supplied must be the one used: %v", err)
	}
}

func TestTheEnvironmentCannotInstallAProxy(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"version":"2.0.0"}`))
	}))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"version":"2.0.0"}`))
	}))
	t.Cleanup(secure.Close)
	trusted := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw})

	cases := []struct {
		env     string
		address string
		ca      []byte
	}{
		{"BAO_PROXY_ADDR", plain.URL, nil},
		{"HTTPS_PROXY", secure.URL, trusted},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, "http://127.0.0.1:1")
			client, err := New(Options{Address: tc.address, Token: "t", CACert: tc.ca})
			if err != nil {
				t.Fatal(err)
			}
			transport, ok := client.client.CloneConfig().HttpClient.Transport.(*http.Transport)
			if !ok {
				t.Fatal("the client carries no HTTP transport")
			}
			if transport.Proxy != nil {
				t.Fatalf("%s must not reach the connection", tc.env)
			}
			if _, err := client.Health(context.Background()); err != nil {
				t.Fatalf("the address the caller supplied must be the one dialled: %v", err)
			}
		})
	}
}

func TestCASerialWithoutACAReportsBothSentinels(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	})
	_, err := client.CASerial(context.Background(), "root", "pki")
	if !errors.Is(err, pki.ErrNoAuthority) {
		t.Fatalf("err = %v, want pki.ErrNoAuthority", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound as well", err)
	}
}

func TestEnsureMountTreatsANullMountListAsNoMounts(t *testing.T) {
	client, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sys/mounts" {
			_, _ = w.Write([]byte("null"))
			return
		}
		w.WriteHeader(204)
	})
	if err := client.EnsureMount(context.Background(), "alice.default", "pki", "pki"); err != nil {
		t.Fatal(err)
	}
	last := (*seen)[len(*seen)-1]
	if last.Path != "/v1/sys/mounts/pki" || !strings.Contains(last.Body, `"type":"pki"`) {
		t.Fatalf("request = %+v", last)
	}
}

func TestEnsureNamespaceCreatesOnlyWhenAbsentAndCarriesTheToken(t *testing.T) {
	exists := false
	client, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && exists:
			envelope(w, map[string]any{"path": "alice.default/"})
		case r.Method == http.MethodGet:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"errors":["namespace not found"]}`))
		default:
			exists = true
			envelope(w, map[string]any{"path": "alice.default/"})
		}
	})
	if err := client.EnsureNamespace(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureNamespace(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, rec := range *seen {
		if rec.Method == http.MethodGet {
			continue
		}
		creates++
		if rec.Method != http.MethodPut || rec.Path != "/v1/sys/namespaces/alice.default" {
			t.Fatalf("create = %s %s, want PUT /v1/sys/namespaces/alice.default", rec.Method, rec.Path)
		}
		if rec.Token != "vault-token" {
			t.Fatalf("create token = %q, want the client's token on the create", rec.Token)
		}
	}
	if creates != 1 {
		t.Fatalf("creates = %d, want exactly one: the second pass reads it present", creates)
	}
}

func TestTheRootNamespaceCarriesNoHeader(t *testing.T) {
	client, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sys/mounts" {
			envelope(w, map[string]any{})
			return
		}
		w.WriteHeader(204)
	})
	if err := client.EnsureMount(context.Background(), "", "pki", "pki"); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].Namespace != "" {
		t.Fatalf("namespace header = %q, want it absent for the root", (*seen)[0].Namespace)
	}
}

func TestIssueSendsIPsInTheirOwnField(t *testing.T) {
	client, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		envelope(w, map[string]any{"certificate": "LEAF", "serial_number": "01"})
	})
	cert, err := client.Issue(context.Background(), "alice.default", "pki", "denopod", pki.CertRequest{
		CommonName: "pds.default.alice.svc.kcp.local",
		AltNames:   []string{"pds.default.alice.svc.kcp.local"},
		IPSANs:     []string{"127.0.0.1", "::1"},
		TTL:        "720h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cert.Certificate != "LEAF" || cert.Serial != "01" {
		t.Fatalf("issue answered %+v", cert)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte((*seen)[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["ip_sans"] != "127.0.0.1,::1" {
		t.Fatalf("ip_sans = %v, want the loopback pair in its own field", body["ip_sans"])
	}
	if alt, _ := body["alt_names"].(string); strings.Contains(alt, "127.0.0.1") {
		t.Fatalf("alt_names = %v carries an address, which OpenBao reads as a DNS name and refuses", body["alt_names"])
	}
}
