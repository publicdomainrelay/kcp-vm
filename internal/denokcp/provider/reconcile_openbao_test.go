package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

type stubOpenBao struct {
	mu sync.Mutex

	requests []string

	namespaces map[string]bool

	serial string

	chain string
}

func newStubOpenBao(t *testing.T) (*stubOpenBao, *httptest.Server) {
	t.Helper()
	stub := &stubOpenBao{namespaces: map[string]bool{}}
	stub.serial = certSerial(t)
	stub.chain = "INTERMEDIATE-PEM\nROOT-PEM"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.requests = append(stub.requests, r.Method+" "+r.URL.Path)
		stub.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/sys/mounts":
			_, _ = w.Write([]byte(`{"data":{"pki/":{"type":"pki"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/sys/namespaces/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1/sys/namespaces/")
			if r.Method == http.MethodGet && !stub.namespaces[name] {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":["namespace not found"]}`))
				return
			}
			stub.namespaces[name] = true
			_, _ = w.Write([]byte(`{"data":{"path":"ok"}}`))
		case r.URL.Path == "/v1/pki/cert/ca":
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"certificate": stub.certPEM(t)}})
			_, _ = w.Write(body)
		case r.URL.Path == "/v1/pki/ca_chain":
			_, _ = w.Write([]byte(stub.chain))
		case strings.HasPrefix(r.URL.Path, "/v1/pki/roles/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("the stub was asked for %s %s, which the test does not answer", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return stub, server
}

func (s *stubOpenBao) certPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(0x2b1d),
		Subject:               pkix.Name{CommonName: "kcp-mesh-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func certSerial(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x2b1d),
		Subject:      pkix.Name{CommonName: "kcp-mesh-root"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	_ = tmpl
	_ = key
	return "2B:1D"
}

func (s *stubOpenBao) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func openBaoObject(name, namespace, path string, finalizers ...string) *v1alpha1.OpenBao {
	return &v1alpha1.OpenBao{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: finalizers,
			Generation: 1,
		},
		Spec: v1alpha1.OpenBaoSpec{Namespace: path},
	}
}

func openBaoProvider(t *testing.T, address string) (*Provider, *fakeRuntime) {
	t.Helper()
	runtime := newFakeRuntime()
	p, err := New(Options{
		Registry:       runtime,
		Runtime:        runtime,
		RestConfig:     &rest.Config{Host: "https://kcp"},
		OpenBaoAddress: address,
		WriteStatus:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, runtime
}

func TestReconcileOpenBaoReportsAnAbsentVault(t *testing.T) {
	p, runtime := openBaoProvider(t, "")
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "openbao"}
	obj := openBaoObject("openbao", "default", "alice.default")
	runtime.openBaos = []v1alpha1.OpenBao{*obj}

	if _, _, err := p.reconcileOpenBao(context.Background(), ref, obj); err != nil {
		t.Fatal(err)
	}
	status := runtime.openBaoStatus[ref]
	if status.Ready {
		t.Fatal("an object reported ready with no OpenBao configured")
	}
	if len(status.Conditions) != 1 || status.Conditions[0].Reason != "NoOpenBao" {
		t.Fatalf("conditions = %v, want the NoOpenBao reason", status.Conditions)
	}
	if len(runtime.openBaoFinalizers[ref]) != 0 {
		t.Fatal("a finalizer was added for an object nothing provisioned, so deleting it would wait on a vault that was never reached")
	}
}

func TestReconcileOpenBaoProvisionsTheNamespacesAuthority(t *testing.T) {
	stub, server := newStubOpenBao(t)
	p, runtime := openBaoProvider(t, server.URL)
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "openbao"}
	obj := openBaoObject("openbao", "default", "alice.default")
	runtime.openBaos = []v1alpha1.OpenBao{*obj}

	if _, _, err := p.reconcileOpenBao(context.Background(), ref, obj); err != nil {
		t.Fatal(err)
	}
	status := runtime.openBaoStatus[ref]
	if !status.Ready {
		t.Fatalf("the object is not ready: %s", status.Message)
	}
	if status.Namespace != "alice.default" {
		t.Fatalf("status.namespace = %q, want the declared path", status.Namespace)
	}
	if status.Serial == "" {
		t.Fatal("status.serial is empty: a mount holding a CA reported none, and the next run would generate a second root")
	}
	if status.Chain == "" {
		t.Fatal("status.chain is empty, so a peer has nothing to verify a leaf from this namespace against")
	}
	if len(runtime.openBaoFinalizers[ref]) != 1 || runtime.openBaoFinalizers[ref][0] != v1alpha1.FinalizerOpenBao {
		t.Fatalf("finalizers = %v, want the OpenBao one", runtime.openBaoFinalizers[ref])
	}
	var touchedNamespace bool
	for _, call := range stub.calls() {
		if call == "PUT /v1/sys/namespaces/alice.default" {
			touchedNamespace = true
		}
	}
	if !touchedNamespace {
		t.Fatalf("the namespace was never created: %v", stub.calls())
	}
}

// ponytail: the ambiguity is reported rather than resolved, and the stub is what
// proves it: not one request reaches the vault, so neither object half-provisions
// an authority the workloads would then be split across.
func TestReconcileOpenBaoRefusesTwoAuthoritiesInOneNamespace(t *testing.T) {
	stub, server := newStubOpenBao(t)
	p, runtime := openBaoProvider(t, server.URL)
	runtime.openBaos = []v1alpha1.OpenBao{
		*openBaoObject("one", "default", "alice.default"),
		*openBaoObject("two", "default", "alice.other"),
	}
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "one"}

	if _, _, err := p.reconcileOpenBao(context.Background(), ref, &runtime.openBaos[0]); err != nil {
		t.Fatal(err)
	}
	status := runtime.openBaoStatus[ref]
	if status.Ready {
		t.Fatal("an object reported ready while a second one names the same namespace")
	}
	if len(status.Conditions) != 1 || status.Conditions[0].Type != v1alpha1.OpenBaoConditionAmbiguous {
		t.Fatalf("conditions = %v, want the Ambiguous condition", status.Conditions)
	}
	if calls := stub.calls(); len(calls) != 0 {
		t.Fatalf("the vault was asked to do %v, want nothing while the namespace is ambiguous", calls)
	}
}

func TestReconcileOpenBaoDeletesItsNamespaceAndReleasesTheFinalizer(t *testing.T) {
	stub, server := newStubOpenBao(t)
	p, runtime := openBaoProvider(t, server.URL)
	ref := Ref{LogicalCluster: "root:alice", Namespace: "default", Name: "openbao"}
	obj := openBaoObject("openbao", "default", "alice.default", v1alpha1.FinalizerOpenBao)
	runtime.openBaos = []v1alpha1.OpenBao{*obj}
	now := metav1.Now()
	obj.DeletionTimestamp = &now

	_, terminal, err := p.reconcileOpenBao(context.Background(), ref, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !terminal {
		t.Fatal("a deleted object was not reported terminal, so it would be requeued forever")
	}
	if len(runtime.openBaoFinalizers[ref]) != 0 {
		t.Fatal("the finalizer was not released")
	}
	deleted := false
	for _, call := range stub.calls() {
		if call == "DELETE /v1/sys/namespaces/alice.default" {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("the OpenBao namespace was not deleted: %v", stub.calls())
	}
}
