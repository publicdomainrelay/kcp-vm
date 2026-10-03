//go:build linux

package clusterprovider

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kubeconfig"
)

const testTenantServer = "https://tenant-a.localhost:6443"

type keyForm int

const (
	keyPKCS8 keyForm = iota
	keySEC1
	keyPKCS1
)

type testCA struct {
	cert *x509.Certificate
	key  crypto.Signer

	certPEM []byte
	keyPEM  []byte
}

func newTestCA(t *testing.T, commonName string) *testCA {
	t.Helper()
	return newTestCAInForm(t, commonName, keyPKCS8)
}

func newTestCAInForm(t *testing.T, commonName string, form keyForm) *testCA {
	t.Helper()
	var signer crypto.Signer
	var keyPEM []byte
	switch form {
	case keyPKCS1:
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate the RSA key for %s: %v", commonName, err)
		}
		signer = key
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	case keySEC1:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate the EC key for %s: %v", commonName, err)
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshal the EC key for %s: %v", commonName, err)
		}
		signer = key
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	default:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate the EC key for %s: %v", commonName, err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatalf("marshal the PKCS#8 key for %s: %v", commonName, err)
		}
		signer = key
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("draw the serial for %s: %v", commonName, err)
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
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		t.Fatalf("sign %s: %v", commonName, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse %s: %v", commonName, err)
	}
	return &testCA{
		cert:    cert,
		key:     signer,
		certPEM: pemCert(der),
		keyPEM:  keyPEM,
	}
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func seedClusterTLS(t *testing.T, clientCA, serverCA *testCA) string {
	t.Helper()
	dataDir := t.TempDir()
	tlsDir := filepath.Join(dataDir, "server", "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", tlsDir, err)
	}
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
	return dataDir
}

func mintOptions(dataDir string) CredentialOptions {
	return CredentialOptions{
		DataDir:       dataDir,
		Server:        testTenantServer,
		CommonName:    SuperuserName,
		Organizations: []string{SuperuserOrganization},
	}
}

func mintInto(t *testing.T, dataDir, server string) *rest.Config {
	t.Helper()
	o := mintOptions(dataDir)
	o.Server = server
	cfg, err := MintTenantCredential(o)
	if err != nil {
		t.Fatalf("MintTenantCredential: %v", err)
	}
	return cfg
}

func leafOf(t *testing.T, cfg *rest.Config) *x509.Certificate {
	t.Helper()
	cert, err := parseCertificate(cfg.TLSClientConfig.CertData)
	if err != nil {
		t.Fatalf("the credential's own certificate does not parse: %v", err)
	}
	return cert
}

func verifyAgainst(t *testing.T, ca *testCA, cert *x509.Certificate) [][]*x509.Certificate {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.certPEM) {
		t.Fatalf("the test CA does not load into a pool")
	}
	chains, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatalf("the certificate does not verify against the CA it was signed with, so an "+
			"apiserver trusting that CA would refuse it as an unknown authority: %v", err)
	}
	return chains
}

func TestTheMintedCredentialChainsToTheClientCAAndCarriesTheSuperuserIdentity(t *testing.T) {
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	cfg := mintInto(t, seedClusterTLS(t, clientCA, serverCA), testTenantServer)

	leaf := leafOf(t, cfg)
	if leaf.Subject.CommonName != SuperuserName {
		t.Fatalf("the credential authenticates as %q and the tenant grant's carve-out names %q",
			leaf.Subject.CommonName, SuperuserName)
	}
	if len(leaf.Subject.Organization) != 1 || leaf.Subject.Organization[0] != SuperuserOrganization {
		t.Fatalf("the credential carries the groups %v; without %q the apiserver sees an "+
			"ordinary unprivileged user and every object the tenant bootstrap applies is refused",
			leaf.Subject.Organization, SuperuserOrganization)
	}
	if leaf.IsCA {
		t.Fatal("the credential is itself a CA: it would sign certificates every apiserver " +
			"trusting the ClientCA accepts, which is a wider thing than this provider holds")
	}
	clientAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			clientAuth = true
		}
	}
	if !clientAuth {
		t.Fatalf("the credential's extended key usage is %v, so the apiserver refuses it at "+
			"authentication even though it chains", leaf.ExtKeyUsage)
	}
	if chains := verifyAgainst(t, clientCA, leaf); len(chains) != 1 {
		t.Fatalf("the credential verifies through %d chains, not the one CA that signed it", len(chains))
	}
	if _, err := tls.X509KeyPair(cfg.TLSClientConfig.CertData, cfg.TLSClientConfig.KeyData); err != nil {
		t.Fatalf("the certificate and the key are not a pair: %v", err)
	}
	if cfg.Host != testTenantServer {
		t.Fatalf("the credential dials %q and it was minted for %q", cfg.Host, testTenantServer)
	}
	if !bytes.Equal(cfg.TLSClientConfig.CAData, serverCA.certPEM) {
		t.Fatal("the credential does not carry the cluster's server CA, so the apiserver it " +
			"dials could not be verified")
	}
	if cfg.TLSClientConfig.Insecure {
		t.Fatal("the credential was built with server verification switched off")
	}
	if cfg.BearerToken != "" {
		t.Fatal("the credential carries a bearer token as well as a certificate; a token is a " +
			"second way in, and the tenant route is a client certificate")
	}
}

func TestNoTwoMintedCredentialsShareAKeyOrASerial(t *testing.T) {
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	dataDir := seedClusterTLS(t, clientCA, serverCA)

	first := mintInto(t, dataDir, testTenantServer)
	second := mintInto(t, dataDir, testTenantServer)

	one, two := leafOf(t, first), leafOf(t, second)
	if one.SerialNumber.Cmp(two.SerialNumber) == 0 {
		t.Fatalf("two mints drew the serial %v: a serial is how one credential is told from "+
			"another, so a repeat makes one credential stand in for the other", one.SerialNumber)
	}
	oneKey, ok := one.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("the credential's key is a %T", one.PublicKey)
	}
	twoKey, ok := two.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("the second credential's key is a %T", two.PublicKey)
	}
	if oneKey.Equal(twoKey) {
		t.Fatal("two mints produced the same key pair, so a credential copied once is every " +
			"credential this provider will ever hold")
	}
	if bytes.Equal(first.TLSClientConfig.KeyData, second.TLSClientConfig.KeyData) {
		t.Fatal("two mints produced the same private key")
	}
	if bytes.Equal(first.TLSClientConfig.CertData, second.TLSClientConfig.CertData) {
		t.Fatal("two mints produced the same certificate")
	}
}

func TestTheMintRefusesWithoutTheClientCA(t *testing.T) {
	unseeded := t.TempDir()

	noCert := seedClusterTLS(t, newTestCA(t, "k3s-client-ca"), newTestCA(t, "k3s-server-ca"))
	if err := os.Remove(filepath.Join(noCert, "server", "tls", "client-ca.crt")); err != nil {
		t.Fatalf("remove client-ca.crt: %v", err)
	}

	noKey := seedClusterTLS(t, newTestCA(t, "k3s-client-ca"), newTestCA(t, "k3s-server-ca"))
	if err := os.Remove(filepath.Join(noKey, "server", "tls", "client-ca.key")); err != nil {
		t.Fatalf("remove client-ca.key: %v", err)
	}

	for _, tc := range []struct{ name, dir string }{
		{"a data dir that was never seeded", unseeded},
		{"no client-ca.crt", noCert},
		{"no client-ca.key", noKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MintTenantCredential(mintOptions(tc.dir))
			if !errors.Is(err, ErrNoClientCA) {
				t.Fatalf("the refusal is %v, and the caller reading it cannot tell that there is "+
					"no credential to mint here", err)
			}
			if errors.Is(err, ErrCredentialSign) || errors.Is(err, ErrCredentialDoesNotChain) {
				t.Fatalf("a missing CA reached the signing path: %v", err)
			}
		})
	}
}

func TestTheMintRefusesAClientCAFileThatIsNotASigningPair(t *testing.T) {
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")

	notACA := seedClusterTLS(t, clientCA, serverCA)
	leafDER := leafDERFor(t, clientCA, "not-a-ca.localhost")
	if err := os.WriteFile(filepath.Join(notACA, "server", "tls", "client-ca.crt"), pemCert(leafDER), 0o600); err != nil {
		t.Fatalf("write the leaf: %v", err)
	}

	notPEM := seedClusterTLS(t, clientCA, serverCA)
	if err := os.WriteFile(filepath.Join(notPEM, "server", "tls", "client-ca.crt"), []byte("no pem here"), 0o600); err != nil {
		t.Fatalf("write the garbage: %v", err)
	}

	noServerCA := seedClusterTLS(t, clientCA, serverCA)
	if err := os.Remove(filepath.Join(noServerCA, "server", "tls", "server-ca.crt")); err != nil {
		t.Fatalf("remove server-ca.crt: %v", err)
	}

	serverCAIsALeaf := seedClusterTLS(t, clientCA, serverCA)
	if err := os.WriteFile(filepath.Join(serverCAIsALeaf, "server", "tls", "server-ca.crt"), pemCert(leafDER), 0o600); err != nil {
		t.Fatalf("write the leaf as the server CA: %v", err)
	}

	for _, tc := range []struct {
		name string
		dir  string
		want error
	}{
		{"a certificate that is not a CA", notACA, ErrClientCAUnusable},
		{"a file that holds no PEM", notPEM, ErrClientCAUnusable},
		{"no server CA", noServerCA, ErrNoServerCA},
		{"a server CA that is a leaf", serverCAIsALeaf, ErrNoServerCA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MintTenantCredential(mintOptions(tc.dir))
			if !errors.Is(err, tc.want) {
				t.Fatalf("the refusal is %v, and the caller reading it cannot tell what to look at", err)
			}
		})
	}
}

func TestAKeyFromAnotherCARefusesBeforeAnythingIsSigned(t *testing.T) {
	mine := newTestCA(t, "k3s-client-ca")
	other := newTestCA(t, "another-clusters-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")

	dataDir := seedClusterTLS(t, mine, serverCA)
	if err := os.WriteFile(filepath.Join(dataDir, "server", "tls", "client-ca.key"), other.keyPEM, 0o600); err != nil {
		t.Fatalf("swap the key: %v", err)
	}

	_, err := MintTenantCredential(mintOptions(dataDir))
	if !errors.Is(err, ErrClientCAKeyMismatch) {
		t.Fatalf("a re-seed that replaced half the CA pair is reported as %v", err)
	}
	if errors.Is(err, ErrCredentialSign) || errors.Is(err, ErrCredentialDoesNotChain) {
		t.Fatalf("the mismatch surfaced as a signing or a chain failure, which reads as an "+
			"authentication bug at the apiserver rather than as the pair on disk: %v", err)
	}
	if !strings.Contains(err.Error(), mine.cert.Subject.CommonName) {
		t.Fatalf("the refusal does not name the certificate on disk, so nothing says which pair "+
			"to look at: %v", err)
	}
	if !strings.Contains(err.Error(), "client-ca.key") {
		t.Fatalf("the refusal does not name the key file: %v", err)
	}
}

func TestTheMintRefusesCallerInputItCannotHonour(t *testing.T) {
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")

	for _, tc := range []struct {
		name string
		bend func(*CredentialOptions)
		want error
	}{
		{"no data dir", func(o *CredentialOptions) { o.DataDir = "" }, ErrNoCredentialDataDir},
		{"an empty CommonName", func(o *CredentialOptions) { o.CommonName = "" }, ErrNoCredentialCommonName},
		{"no organization", func(o *CredentialOptions) { o.Organizations = nil }, ErrNoCredentialOrganization},
		{"an empty organization", func(o *CredentialOptions) { o.Organizations = []string{""} }, ErrNoCredentialOrganization},
		{"an empty server", func(o *CredentialOptions) { o.Server = "" }, ErrNoCredentialServer},
		{"a server with no host", func(o *CredentialOptions) { o.Server = "https:///api" }, ErrNoCredentialServer},
		{"a plaintext server", func(o *CredentialOptions) { o.Server = "http://tenant-a.localhost:6443" }, ErrCredentialServerNotHTTPS},
		{"a server with no scheme", func(o *CredentialOptions) { o.Server = "tenant-a.localhost:6443" }, ErrCredentialServerNotHTTPS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := mintOptions(seedClusterTLS(t, clientCA, serverCA))
			tc.bend(&o)
			cfg, err := MintTenantCredential(o)
			if !errors.Is(err, tc.want) {
				t.Fatalf("the refusal is %v", err)
			}
			if cfg != nil {
				t.Fatal("a refused mint returned a credential alongside the error")
			}
		})
	}
}

func TestTheClientCAKeyFormIsNotADecisionPoint(t *testing.T) {
	serverCA := newTestCA(t, "k3s-server-ca")
	for _, tc := range []struct {
		name string
		form keyForm
	}{
		{"PKCS#8", keyPKCS8},
		{"SEC 1", keySEC1},
		{"PKCS#1", keyPKCS1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientCA := newTestCAInForm(t, "k3s-client-ca", tc.form)
			cfg := mintInto(t, seedClusterTLS(t, clientCA, serverCA), testTenantServer)
			if leaf := leafOf(t, cfg); leaf.Subject.CommonName != SuperuserName {
				t.Fatalf("the credential authenticates as %q", leaf.Subject.CommonName)
			}
		})
	}
}

type handshakeResult struct {
	client tls.ConnectionState
	server tls.ConnectionState
}

func handshake(t *testing.T, cfg *rest.Config, srv *tls.Config) (handshakeResult, error) {
	t.Helper()
	tlsCfg, err := rest.TLSConfigFor(cfg)
	if err != nil {
		t.Fatalf("client-go refuses to build TLS from the credential this test just minted: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("client-go built no TLS at all from a credential that carries a CA and a client " +
			"certificate, so there is nothing here that verifies the apiserver")
	}
	u, err := url.Parse(cfg.Host)
	if err != nil {
		t.Fatalf("parse %q: %v", cfg.Host, err)
	}
	tlsCfg.ServerName = u.Hostname()

	clientConn, serverConn := net.Pipe()
	var wg sync.WaitGroup
	var out handshakeResult
	var serverErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		s := tls.Server(serverConn, srv)
		if err := s.Handshake(); err != nil {
			serverErr = fmt.Errorf("the server side: %w", err)
			s.Close()
			return
		}
		out.server = s.ConnectionState()
		s.Read(make([]byte, 1))
		s.Close()
	}()

	c := tls.Client(clientConn, tlsCfg)
	err = c.Handshake()
	if err == nil {
		out.client = c.ConnectionState()
	}
	c.Close()
	wg.Wait()
	if err != nil {
		return out, err
	}
	return out, serverErr
}

func serverFor(t *testing.T, signingCA *testCA, host string, clientCA *testCA) *tls.Config {
	t.Helper()
	der, key := leafFor(t, signingCA, host, x509.ExtKeyUsageServerAuth)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(clientCA.certPEM) {
		t.Fatal("the test client CA does not load into a pool")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}
}

func leafDERFor(t *testing.T, ca *testCA, host string) []byte {
	t.Helper()
	der, _ := leafFor(t, ca, host, x509.ExtKeyUsageServerAuth)
	return der
}

func leafFor(t *testing.T, ca *testCA, host string, usage x509.ExtKeyUsage) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the leaf key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("draw the leaf serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{usage},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, key.Public(), ca.key)
	if err != nil {
		t.Fatalf("sign the leaf: %v", err)
	}
	return der, key
}

func TestTheCredentialVerifiesTheServerAndTheServerVerifiesTheCredential(t *testing.T) {
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	cfg := mintInto(t, seedClusterTLS(t, clientCA, serverCA), testTenantServer)

	res, err := handshake(t, cfg, serverFor(t, serverCA, "tenant-a.localhost", clientCA))
	if err != nil {
		t.Fatalf("the credential cannot complete a handshake with a server presenting a "+
			"certificate for the host its own URL names, so it would never reach the apiserver "+
			"it was minted for: %v", err)
	}
	if len(res.client.VerifiedChains) == 0 {
		t.Fatal("the client accepted the server without recording a verified chain, so the " +
			"server CA on this credential verified nothing")
	}
	if len(res.server.PeerCertificates) == 0 {
		t.Fatal("the credential completed the handshake without sending a certificate, so the " +
			"apiserver would authenticate it as anonymous")
	}
	seen := res.server.PeerCertificates[0].Subject
	if seen.CommonName != SuperuserName {
		t.Fatalf("the server reads the username %q and the tenant grant's carve-out names %q",
			seen.CommonName, SuperuserName)
	}
	if len(seen.Organization) != 1 || seen.Organization[0] != SuperuserOrganization {
		t.Fatalf("the server reads the groups %v and the superuser group is %q",
			seen.Organization, SuperuserOrganization)
	}
}

func TestTheCredentialRefusesAServerThatIsNotTheOneItsURLNames(t *testing.T) {
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	strangerCA := newTestCA(t, "a-ca-this-cluster-never-seeded")
	cfg := mintInto(t, seedClusterTLS(t, clientCA, serverCA), testTenantServer)

	_, err := handshake(t, cfg, serverFor(t, serverCA, "tenant-b.localhost", clientCA))
	if err == nil {
		t.Fatal("the credential accepted a certificate for tenant-b.localhost while its URL " +
			"names tenant-a.localhost, so the URL's host is not the name being checked")
	}
	if !strings.Contains(err.Error(), "tenant-a.localhost") {
		t.Fatalf("the refusal does not name the host the URL carries: %v", err)
	}

	_, err = handshake(t, cfg, serverFor(t, strangerCA, "tenant-a.localhost", clientCA))
	if err == nil {
		t.Fatal("the credential accepted a server certificate signed by a CA that is not the " +
			"cluster's server CA, so the CA it carries verifies nothing")
	}
}

func renderCertKubeconfig(t *testing.T, cluster string, cfg *rest.Config) string {
	t.Helper()
	out := clientcmdapi.NewConfig()
	out.Clusters[cluster] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.TLSClientConfig.CAData,
	}
	out.AuthInfos[cluster] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.TLSClientConfig.CertData,
		ClientKeyData:         cfg.TLSClientConfig.KeyData,
	}
	out.Contexts[cluster] = &clientcmdapi.Context{Cluster: cluster, AuthInfo: cluster}
	out.CurrentContext = cluster
	doc, err := clientcmd.Write(*out)
	if err != nil {
		t.Fatalf("render the credential into a kubeconfig: %v", err)
	}
	return string(doc)
}

func TestTheTenantCredentialCannotReachAUser(t *testing.T) {
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	cfg := mintInto(t, seedClusterTLS(t, clientCA, serverCA), testTenantServer)

	mintDir := t.TempDir()
	if err := WriteMintKubeconfig(mintDir, "tenant-a", cfg); err == nil {
		t.Fatal("the bootstrap's own writer accepted a certificate credential, so the " +
			"provider's cluster-admin-equivalent key would sit in the file the gateway reads back")
	}
	if names, _ := os.ReadDir(mintDir); len(names) != 0 {
		t.Fatalf("a refused write left %d files behind", len(names))
	}

	rendered := renderCertKubeconfig(t, "tenant-a", cfg)
	if !strings.Contains(rendered, "client-certificate-data") {
		t.Fatalf("the renderer dropped the client certificate, so this test says nothing about "+
			"the boundary: %s", rendered)
	}
	err := kubeconfig.AssertClean(rendered)
	if err == nil {
		t.Fatal("kubeconfig.AssertClean accepted a document carrying the provider's client " +
			"certificate and key: the tenant credential would reach a user as a file that " +
			"authenticates as the superuser")
	}
	if !strings.Contains(err.Error(), "client-certificate-data") {
		t.Fatalf("AssertClean refused the document without naming what it found: %v", err)
	}

	device, err := kubeconfig.Build(kubeconfig.Input{
		ClusterName:      "tenant-a",
		Server:           testTenantServer,
		CACertPEM:        string(serverCA.certPEM),
		ExecCommand:      "socialweb-gateway",
		ExecArgs:         []string{"--device-credential"},
		DeviceCredential: "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJkaWQ6a2V5OnpEbmEifQ.signature",
		DeviceKeyEnvName: "SOCIALWEB_DEVICE_KEY",
	})
	if err != nil {
		t.Fatalf("kubeconfig.Build: %v", err)
	}
	if err := kubeconfig.AssertClean(device); err != nil {
		t.Fatalf("AssertClean refused the document the gateway hands a device, so the refusal "+
			"above is a check that refuses everything rather than a boundary: %v", err)
	}
}
