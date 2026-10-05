package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
)

const testCluster = "tenant-a"

const testServer = "https://tenant-a.localhost:6443"

type testCA struct {
	certPEM []byte
	keyPEM  []byte
}

func newTestCA(t *testing.T, commonName string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the %s key: %v", commonName, err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("draw the %s serial: %v", commonName, err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatalf("sign %s: %v", commonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal the %s key: %v", commonName, err)
	}
	return testCA{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}

func seedClusterTLS(t *testing.T, stateDir, cluster string) {
	t.Helper()
	dataDir, err := clusterprovider.ClusterDataDir(stateDir, cluster)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	tlsDir := filepath.Join(dataDir, "server", "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", tlsDir, err)
	}
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"client-ca.crt", clientCA.certPEM},
		{"client-ca.key", clientCA.keyPEM},
		{"server-ca.crt", serverCA.certPEM},
	} {
		if err := os.WriteFile(filepath.Join(tlsDir, f.name), f.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
}

func setEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for k, v := range values {
		t.Setenv(k, v)
	}
}

func runEnv(stateDir, outDir string, extra map[string]string) map[string]string {
	env := map[string]string{
		"SOCIALWEB_TENANTCREDENTIAL_STATE_DIR":     stateDir,
		"SOCIALWEB_TENANTCREDENTIAL_CLUSTER":       testCluster,
		"SOCIALWEB_TENANTCREDENTIAL_SERVER":        testServer,
		"SOCIALWEB_TENANTCREDENTIAL_COMMON_NAME":   clusterprovider.SuperuserName,
		"SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS": clusterprovider.SuperuserOrganization,
		"SOCIALWEB_TENANTCREDENTIAL_OUT_DIR":       outDir,
	}
	for k, v := range extra {
		if v == "" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return env
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out), runErr
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestTheHappyPathMintsAndWritesAUsableCredentialAndPrintsOnlyASummary(t *testing.T) {
	stateDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "cred")
	seedClusterTLS(t, stateDir, testCluster)
	setEnv(t, runEnv(stateDir, outDir, nil))

	out, err := captureStdout(t, run)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := strings.Count(out, "\n"); got != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout is %d line(s), want exactly one: the operator reads one summary line and "+
			"nothing else, got %q", got, out)
	}
	for _, want := range []string{testCluster, clusterprovider.SuperuserName,
		clusterprovider.SuperuserOrganization, "ca.crt", "tls.crt", "tls.key"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the summary does not name %q: %q", want, out)
		}
	}
	for _, secret := range []string{"PRIVATE KEY", "BEGIN CERTIFICATE"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the summary carries %q, which is key or certificate material: %q", secret, out)
		}
	}

	certPEM := readFile(t, filepath.Join(outDir, "tls.crt"))
	keyPEM := readFile(t, filepath.Join(outDir, "tls.key"))
	caPEM := readFile(t, filepath.Join(outDir, "ca.crt"))

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("tls.crt and tls.key are not a pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse the minted leaf: %v", err)
	}
	if leaf.Subject.CommonName != clusterprovider.SuperuserName {
		t.Fatalf("the written certificate authenticates as %q, want %q", leaf.Subject.CommonName,
			clusterprovider.SuperuserName)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("ca.crt holds no certificate, so the apiserver the credential dials could not be " +
			"verified")
	}
	for _, name := range []string{"ca.crt", "tls.crt", "tls.key"} {
		info, err := os.Stat(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s is mode %o, want 0600: the key is readable by nobody but its owner", name, perm)
		}
		left, err := os.ReadDir(outDir)
		if err != nil {
			t.Fatalf("read %s: %v", outDir, err)
		}
		if len(left) != 3 {
			t.Fatalf("the out dir holds %d entries, want the three files with no temporary left "+
				"behind", len(left))
		}
	}
}

func TestANonHTTPSServerIsRefused(t *testing.T) {
	setEnv(t, runEnv(t.TempDir(), filepath.Join(t.TempDir(), "cred"), map[string]string{
		"SOCIALWEB_TENANTCREDENTIAL_SERVER": "http://tenant-a.localhost:6443",
	}))
	_, err := captureStdout(t, run)
	if err == nil {
		t.Fatal("a plaintext server URL was accepted: the apiserver serves nothing else, and the " +
			"credential would cross the network in the clear")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("the refusal does not say the URL must be https: %v", err)
	}
}

func TestAMintedIdentityThatIsNotTheOneAskedForIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"a common name the minting path cannot produce",
			map[string]string{"SOCIALWEB_TENANTCREDENTIAL_COMMON_NAME": "gitlab-runner"}, "gitlab-runner"},
		{"an organization the minting path cannot produce",
			map[string]string{"SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS": "gitlab-runner"}, "gitlab-runner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			outDir := filepath.Join(t.TempDir(), "cred")
			seedClusterTLS(t, stateDir, testCluster)
			setEnv(t, runEnv(stateDir, outDir, tc.extra))

			_, err := captureStdout(t, run)
			if err == nil {
				t.Fatal("a credential whose identity is not the one that was asked for was accepted: " +
					"the operator's RBAC binds the requested identity, so a mismatch is a credential " +
					"no rule matches")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not name %q, so an operator cannot tell what differs: %v",
					tc.want, err)
			}
			left, readErr := os.ReadDir(outDir)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatalf("read %s: %v", outDir, readErr)
			}
			if len(left) != 0 {
				t.Fatalf("a refused run wrote %d file(s): the certificate is the artifact, and a "+
					"credential that does not match what was asked for must not reach disk", len(left))
			}
		})
	}
}

func TestExistingFilesAreRefusedRatherThanOverwritten(t *testing.T) {
	outDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outDir, "tls.key"), []byte("an existing key"), 0o600); err != nil {
		t.Fatalf("seed the existing file: %v", err)
	}
	setEnv(t, runEnv(t.TempDir(), outDir, nil))

	_, err := captureStdout(t, run)
	if err == nil {
		t.Fatal("the tool overwrote an existing credential: a silent replacement is exactly the " +
			"failure invariant 20 exists to prevent")
	}
	if !strings.Contains(err.Error(), "tls.key") {
		t.Fatalf("the refusal does not name the file that exists: %v", err)
	}
	if got := readFile(t, filepath.Join(outDir, "tls.key")); string(got) != "an existing key" {
		t.Fatalf("the existing file was modified: %q", got)
	}
}

func TestTheThreeFilesAreWrittenFromAHandBuiltConfig(t *testing.T) {
	dir := t.TempDir()
	restCfg := &rest.Config{TLSClientConfig: rest.TLSClientConfig{
		CAData:   []byte("the server ca"),
		CertData: []byte("the client certificate"),
		KeyData:  []byte("the client key"),
	}}
	paths, err := writeCredential(dir, restCfg)
	if err != nil {
		t.Fatalf("writeCredential: %v", err)
	}
	for i, want := range []struct{ name, data string }{
		{"ca.crt", "the server ca"},
		{"tls.crt", "the client certificate"},
		{"tls.key", "the client key"},
	} {
		if paths[i] != filepath.Join(dir, want.name) {
			t.Fatalf("paths[%d] = %q, want %q", i, paths[i], filepath.Join(dir, want.name))
		}
		if got := readFile(t, paths[i]); string(got) != want.data {
			t.Fatalf("%s = %q, want %q", want.name, got, want.data)
		}
		info, err := os.Stat(paths[i])
		if err != nil {
			t.Fatalf("stat %s: %v", want.name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s is mode %o, want 0600", want.name, perm)
		}
	}
}

func TestAPartialSetIsRefusedAndNothingIsWritten(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *rest.Config
		missing string
	}{
		{"no ca", &rest.Config{TLSClientConfig: rest.TLSClientConfig{
			CertData: []byte("cert"), KeyData: []byte("key")}}, "ca.crt"},
		{"no certificate", &rest.Config{TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("ca"), KeyData: []byte("key")}}, "tls.crt"},
		{"no key", &rest.Config{TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("ca"), CertData: []byte("cert")}}, "tls.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := writeCredential(dir, tc.cfg)
			if err == nil {
				t.Fatal("a partial credential was written: a reader of the directory would find " +
					"something that looks complete and is not")
			}
			if !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("the refusal does not name %q, so an operator cannot tell which file is "+
					"missing: %v", tc.missing, err)
			}
			left, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatalf("read %s: %v", dir, readErr)
			}
			if len(left) != 0 {
				t.Fatalf("a refused write left %d file(s) behind", len(left))
			}
		})
	}
}

func TestACredentialWithNoCertificateIsRefused(t *testing.T) {
	_, err := identityOf(&rest.Config{})
	if err == nil {
		t.Fatal("a credential carrying no client certificate was accepted, so there is no identity " +
			"to check and nothing to write to tls.crt")
	}
}
