package pkiprovisioner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/pki"
)

type fakeClient struct {
	mu sync.Mutex

	delay time.Duration

	rootSerial string

	namespaceSerial map[string]string

	calls []string

	namespaces map[string]bool
}

func newFakeClient() *fakeClient {
	return &fakeClient{namespaces: map[string]bool{}, namespaceSerial: map[string]string{}}
}

func (f *fakeClient) recordLocked(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeClient) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			total++
		}
	}
	return total
}

func (f *fakeClient) EnsureNamespace(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("EnsureNamespace:" + path)
	f.namespaces[path] = true
	return nil
}

func (f *fakeClient) EnsureMount(_ context.Context, namespace, path, kind string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("EnsureMount:" + namespace)
	return nil
}

func (f *fakeClient) GenerateRoot(context.Context, string, string, string, string) (pki.RootCA, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("GenerateRoot")
	f.rootSerial = "AA:BB"
	return pki.RootCA{Certificate: "ROOT-PEM", Serial: "AA:BB"}, nil
}

func (f *fakeClient) CASerial(_ context.Context, namespace, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("CASerial:" + namespace)
	if namespace == "root" {
		return f.rootSerial, nil
	}
	return f.namespaceSerial[namespace], nil
}

func (f *fakeClient) CAChain(_ context.Context, namespace, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("CAChain:" + namespace)
	if namespace == "root" {
		return "ROOT-PEM", nil
	}
	return "INTERMEDIATE-PEM\nROOT-PEM", nil
}

func (f *fakeClient) GenerateIntermediate(context.Context, string, string, string) (pki.IntermediateCSR, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("GenerateIntermediate")
	return pki.IntermediateCSR{CSR: "CSR-PEM"}, nil
}

func (f *fakeClient) SignIntermediate(context.Context, string, string, string, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("SignIntermediate")
	return "SIGNED-PEM", nil
}

func (f *fakeClient) SetSignedIntermediate(_ context.Context, namespace, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("SetSignedIntermediate")
	f.namespaceSerial[namespace] = "CC:DD"
	return nil
}

func (f *fakeClient) WriteRole(context.Context, string, string, string, pki.Role) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("WriteRole")
	return nil
}

func (f *fakeClient) Issue(_ context.Context, namespace, _, _ string, req pki.CertRequest) (pki.Cert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("Issue:" + namespace)
	return pki.Cert{
		Certificate: "LEAF-PEM",
		PrivateKey:  "KEY-PEM",
		CAChain:     []string{"INTERMEDIATE-PEM"},
		Serial:      "EE:FF",
	}, nil
}

func (f *fakeClient) DeleteNamespace(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("DeleteNamespace:" + path)
	return nil
}

func TestEnsureAuthoritySignsOnceAndCaches(t *testing.T) {
	client := newFakeClient()
	provisioner, err := New(Options{Client: client, RootNamespace: "root", Domain: "kcp.local"})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := provisioner.EnsureAuthority(context.Background(), "alice.default")
	if err != nil {
		t.Fatal(err)
	}
	if authority.Chain != "INTERMEDIATE-PEM\nROOT-PEM" {
		t.Fatalf("chain = %q", authority.Chain)
	}
	if authority.CommonName != "alice.default.intermediate" {
		t.Fatalf("common name = %q", authority.CommonName)
	}
	first := len(client.calls)
	if _, err := provisioner.EnsureAuthority(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != first {
		t.Fatal("a cached authority must not be reprovisioned")
	}
}

func TestEnsureAuthorityReusesAnExistingIntermediate(t *testing.T) {
	client := newFakeClient()
	client.rootSerial = "AA:BB"
	client.namespaceSerial["alice.default"] = "CC:DD"
	provisioner, err := New(Options{Client: client, RootNamespace: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.EnsureAuthority(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	for _, call := range client.calls {
		if call == "SignIntermediate" {
			t.Fatal("an existing intermediate must not be signed again")
		}
	}
}

func TestCachedRootPEMIsEmptyUntilSomethingProvisions(t *testing.T) {
	client := newFakeClient()
	provisioner, err := New(Options{Client: client, RootNamespace: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(provisioner.CachedRootPEM()) != 0 {
		t.Fatal("the root must not be read before anything provisions")
	}
	if _, err := provisioner.EnsureAuthority(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	if string(provisioner.CachedRootPEM()) != "ROOT-PEM" {
		t.Fatalf("cached root = %q", provisioner.CachedRootPEM())
	}
}

func TestIssueAlwaysCarriesLoopbackSANs(t *testing.T) {
	client := newFakeClient()
	provisioner, err := New(Options{Client: client, RootNamespace: "root"})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := provisioner.Issue(context.Background(), "alice.default", "pds.default.alice.svc.kcp.local", []string{"pds.default.alice.svc.kcp.local"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Certificate != "LEAF-PEM" {
		t.Fatalf("cert = %+v", cert)
	}
	if LeafChain(cert) != "LEAF-PEM\nINTERMEDIATE-PEM" {
		t.Fatalf("chain = %q", LeafChain(cert))
	}
}

func TestDeleteForgetsTheNamespace(t *testing.T) {
	client := newFakeClient()
	provisioner, err := New(Options{Client: client, RootNamespace: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.EnsureAuthority(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	if err := provisioner.Delete(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	before := len(client.calls)
	if _, err := provisioner.EnsureAuthority(context.Background(), "alice.default"); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) == before {
		t.Fatal("a deleted namespace must be provisioned again")
	}
}

func TestConcurrentEnsureAuthorityProvisionsOncePerNamespace(t *testing.T) {
	client := newFakeClient()
	client.delay = 10 * time.Millisecond
	provisioner, err := New(Options{Client: client, RootNamespace: "root"})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"alice.default", "alice.default", "bob.default", "bob.default"}
	var wg sync.WaitGroup
	for _, path := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			if _, err := provisioner.EnsureAuthority(context.Background(), path); err != nil {
				t.Errorf("EnsureAuthority(%s): %v", path, err)
			}
		}(path)
	}
	wg.Wait()
	if got := client.count("GenerateRoot"); got != 1 {
		t.Fatalf("the root was generated %d times, want one", got)
	}
	if got := client.count("SignIntermediate"); got != 2 {
		t.Fatalf("intermediates were signed %d times, want one per namespace", got)
	}
}
