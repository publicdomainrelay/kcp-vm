package vaultcert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

func requireBao(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts a real openbao and -short was asked for: a skip is not a pass")
	if _, err := exec.LookPath("bao"); err != nil {
		livegate.RequireLocal(t, "bao is not on PATH, so the CERT auth path is not exercised against a real "+
			"vault: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("borrowing a port for the test vault: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func writeClientCA(t *testing.T, dataDir string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the test client CA key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("drawing the test client CA serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "socialweb-test-client-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatalf("signing the test client CA: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling the test client CA key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsDir := clusterstack.TLSDir(dataDir)
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", tlsDir, err)
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"client-ca.crt", certPEM},
		{"client-ca.key", keyPEM},
	} {
		if err := os.WriteFile(filepath.Join(tlsDir, f.name), f.data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", f.name, err)
		}
	}
	return certPEM
}

func startVaultPostgres(t *testing.T) *clusterprovider.Postgres {
	t.Helper()
	for _, bin := range []string{"initdb", "postgres", "psql"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH: the vault's store is this cluster's own embedded-postgres, so there is no vault to start", bin)
		}
	}
	pg, err := clusterprovider.StartPostgres(context.Background(), clusterprovider.PostgresSpec{
		Cluster:  "vaultcert",
		StateDir: t.TempDir(),
		Port:     freePort(t),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	t.Cleanup(func() {
		if err := pg.Stop(context.Background()); err != nil {
			t.Errorf("stopping the vault store: %v", err)
		}
	})
	return pg
}

func startTLSCertVault(t *testing.T, dataDir string, port int, tlsHost string) {
	t.Helper()
	vaultTLS, err := EnsureVaultTLSCertificate(dataDir, tlsHost, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
	}
	spec := clusterstack.Spec{
		DataDir:            dataDir,
		OpenBaoConfig:      clusterstack.OpenBaoConfigPath(dataDir),
		OpenBaoPort:        port,
		OpenBaoTLSCertFile: vaultTLS.CertFile,
		OpenBaoTLSKeyFile:  vaultTLS.KeyFile,
		OpenBaoPostgresDSN: startVaultPostgres(t).VaultDSN(),
	}
	body, err := spec.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig: %v", err)
	}
	if err := os.WriteFile(spec.OpenBaoConfig, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", spec.OpenBaoConfig, err)
	}
	logFile, err := os.Create(filepath.Join(dataDir, "vault.log"))
	if err != nil {
		t.Fatalf("creating the vault log: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "bao", "server", "-config="+spec.OpenBaoConfig)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting bao: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		_ = logFile.Close()
	})
	forwardAlias(t, tlsHost, port)
}

func forwardAlias(t *testing.T, alias string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(alias, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listening on the loopback alias %s: %v", alias, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				up, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}(conn)
		}
	}()
}

func TestTheNodeCertificateLogsInReadsItsOwnSecretAndIsDeniedAnotherNodes(t *testing.T) {
	requireBao(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	clientCAPEM := writeClientCA(t, dataDir)
	port := freePort(t)
	tlsHost := "127.0.0.2"
	startTLSCertVault(t, dataDir, port, tlsHost)

	seal, err := clusterprovider.EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("EnsureVault: %v", err)
	}
	adminAddr := clusterprovider.VaultAddr(port)
	if err := clusterprovider.ApplyClusterVault(ctx, dataDir, seal); err != nil {
		t.Fatalf("ApplyClusterVault: %v", err)
	}
	if err := clusterprovider.ImportClientCA(ctx, dataDir, seal); err != nil {
		t.Fatalf("ImportClientCA: %v", err)
	}

	serverCA := readVaultServerCA(t, dataDir)
	pool, err := openbao.CertPoolFromPEM(serverCA)
	if err != nil {
		t.Fatalf("CertPoolFromPEM: %v", err)
	}
	issuedCA, err := VaultClientCAPEM(ctx, adminAddr, seal.RootToken, pool)
	if err != nil {
		t.Fatalf("VaultClientCAPEM: %v", err)
	}
	wantCA := mustLeaf(t, clientCAPEM)
	gotCA := mustLeaf(t, issuedCA)
	if wantCA.SerialNumber.Cmp(gotCA.SerialNumber) != 0 {
		t.Fatalf("the vault's pki/ca/pem is serial %s and the imported client CA is serial %s, so the "+
			"vault would issue certificates under a different authority than the one it trusts",
			gotCA.SerialNumber, wantCA.SerialNumber)
	}

	vaultURL := "https://" + net.JoinHostPort(tlsHost, strconv.Itoa(port))

	if err := WriteNodeSecret(ctx, adminAddr, seal.RootToken, "node-a", map[string]any{"value": "alpha"}, serverCA); err != nil {
		t.Fatalf("WriteNodeSecret(node-a): %v", err)
	}
	if err := WriteNodeSecret(ctx, adminAddr, seal.RootToken, "node-b", map[string]any{"value": "beta"}, serverCA); err != nil {
		t.Fatalf("WriteNodeSecret(node-b): %v", err)
	}

	certA, err := MintNodeCertificate(ctx, adminAddr, seal.RootToken, "node-a", vaultURL, serverCA)
	if err != nil {
		t.Fatalf("MintNodeCertificate(node-a): %v", err)
	}
	certB, err := MintNodeCertificate(ctx, adminAddr, seal.RootToken, "node-b", vaultURL, serverCA)
	if err != nil {
		t.Fatalf("MintNodeCertificate(node-b): %v", err)
	}

	leafA := mustLeaf(t, certA.CertPEM)
	if leafA.Subject.CommonName != "node-a" {
		t.Fatalf("the minted certificate's common name is %q, want node-a", leafA.Subject.CommonName)
	}
	verifyUnder(t, clientCAPEM, leafA)

	own, err := certA.FetchOwnSecret(ctx)
	if err != nil {
		t.Fatalf("node-a could not read its own secret through CERT auth: %v", err)
	}
	if own["value"] != "alpha" {
		t.Fatalf("node-a read %v, want the secret written for node-a", own)
	}
	if _, err := certA.ReadKV(ctx, vaultlayout.NodeSecretPath("node-b")); !errors.Is(err, openbao.ErrForbidden) {
		t.Fatalf("node-a read node-b's secret path: got %v, want ErrForbidden", err)
	}

	wrongRole := certB
	wrongRole.Metadata.Role = vaultlayout.NodeCertRoleName("node-a")
	if _, err := wrongRole.Login(ctx); err == nil {
		t.Fatalf("node-b's certificate logged in through node-a's CERT role, so allowed_common_names "+
			"does not bind the certificate to its node: role %s", wrongRole.Metadata.Role)
	}

	ownB, err := certB.FetchOwnSecret(ctx)
	if err != nil {
		t.Fatalf("node-b could not read its own secret: %v", err)
	}
	if ownB["value"] != "beta" {
		t.Fatalf("node-b read %v, want the secret written for node-b", ownB)
	}
	if _, err := certB.ReadKV(ctx, vaultlayout.NodeSecretPath("node-a")); !errors.Is(err, openbao.ErrForbidden) {
		t.Fatalf("node-b read node-a's secret path: got %v, want ErrForbidden", err)
	}

	if err := WriteESODemoSecret(ctx, adminAddr, seal.RootToken, map[string]any{"demo": "openbao"}, serverCA); err != nil {
		t.Fatalf("WriteESODemoSecret: %v", err)
	}
	certE, err := MintESOCertificate(ctx, adminAddr, seal.RootToken, vaultlayout.RoleESO, vaultURL, serverCA)
	if err != nil {
		t.Fatalf("MintESOCertificate: %v", err)
	}
	eso, err := certE.ReadKV(ctx, vaultlayout.PathESODemo)
	if err != nil {
		t.Fatalf("the ESO certificate could not read %s: %v", vaultlayout.PathESODemo, err)
	}
	if eso["demo"] != "openbao" {
		t.Fatalf("the ESO certificate read %v, want the demo secret", eso)
	}
	if _, err := certE.ReadKV(ctx, vaultlayout.NodeSecretPath("node-a")); !errors.Is(err, openbao.ErrForbidden) {
		t.Fatalf("the ESO certificate read a per-node secret path: got %v, want ErrForbidden", err)
	}
	if _, err := certE.ReadKV(ctx, vaultlayout.PathClusterIdentity); !errors.Is(err, openbao.ErrForbidden) {
		t.Fatalf("the ESO certificate read the identity bundle: got %v, want ErrForbidden", err)
	}

	esoToken, err := certE.Login(ctx)
	if err != nil {
		t.Fatalf("the ESO certificate could not log in through CERT auth: %v", err)
	}
	esoHTTP, err := certE.tlsClient()
	if err != nil {
		t.Fatalf("the ESO certificate's TLS client: %v", err)
	}
	esoRaw := newRawClient(certE.Metadata.VaultAddr, esoToken, esoHTTP)
	for _, call := range []struct {
		name   string
		method string
		path   string
	}{
		{"lookup-self", http.MethodGet, vaultlayout.PathTokenLookupSelf},
		{"revoke-self", http.MethodPost, vaultlayout.PathTokenRevokeSelf},
	} {
		status, body, err := esoRaw.do(ctx, call.method, call.path, nil)
		if err != nil {
			t.Fatalf("the ESO identity's %s: %v", call.name, err)
		}
		if status < 200 || status > 299 {
			t.Fatalf("the ESO identity's %s -> HTTP %d: %s, so the ESO provider's store "+
				"validation and client close would be denied exactly as the controller logs",
				call.name, status, strings.TrimSpace(string(body)))
		}
	}
	nodeToken, err := certA.Login(ctx)
	if err != nil {
		t.Fatalf("node-a could not log in through CERT auth: %v", err)
	}
	nodeHTTP, err := certA.tlsClient()
	if err != nil {
		t.Fatalf("node-a's TLS client: %v", err)
	}
	status, body, err := newRawClient(certA.Metadata.VaultAddr, nodeToken, nodeHTTP).do(ctx, http.MethodGet, vaultlayout.PathTokenLookupSelf, nil)
	if err != nil {
		t.Fatalf("node-a's lookup-self: %v", err)
	}
	if status != http.StatusForbidden {
		t.Fatalf("node-a's lookup-self -> HTTP %d: %s, want 403: the node policy grants only its own "+
			"secret path, so a self-token call must stay denied and the ESO policy's own grant is the "+
			"least privilege the provider needs", status, strings.TrimSpace(string(body)))
	}

	t.Logf("CERT auth proven against bao %s: node-a serial %s read its own path, was denied "+
		"node-b's, and was denied lookup-self (403, the state the ESO policy had to fix); node-b was "+
		"denied node-a's role; the ESO identity read %s, was denied a node path and the identity "+
		"bundle, and passed lookup-self and revoke-self",
		baoVersion(ctx, adminAddr, serverCA), certA.Serial, vaultlayout.PathESODemo)
}

func TestTheCertificateRoundTripsThroughItsFiles(t *testing.T) {
	c := ClientCertificate{
		Metadata:    ClientMetadata{Identity: "node-a", Role: "node-node-a", VaultAddr: "https://10.0.0.1:20004"},
		CertPEM:     []byte("cert"),
		KeyPEM:      []byte("key"),
		ServerCAPEM: []byte("ca"),
		Serial:      "1",
	}
	dir := t.TempDir()
	if err := c.WriteTo(dir); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Metadata != c.Metadata || string(got.CertPEM) != "cert" || string(got.KeyPEM) != "key" || string(got.ServerCAPEM) != "ca" {
		t.Fatalf("Load returned %+v, want the written certificate", got)
	}
	info, err := os.Stat(filepath.Join(dir, NodeKeyFile))
	if err != nil {
		t.Fatalf("stat %s: %v", NodeKeyFile, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the written private key is mode %04o, want 0600", info.Mode().Perm())
	}
}

func readVaultServerCA(t *testing.T, dataDir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(clusterstack.OpenBaoTLSCAPath(dataDir))
	if err != nil {
		t.Fatalf("reading the vault server CA: %v", err)
	}
	return raw
}

func mustLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	leaf, err := parseLeaf(certPEM)
	if err != nil {
		t.Fatalf("parsing a certificate: %v", err)
	}
	return leaf
}

func verifyUnder(t *testing.T, caPEM []byte, leaf *x509.Certificate) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("the CA holds no CERTIFICATE PEM block")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("the minted certificate does not chain to the imported client CA: %v", err)
	}
}

func baoVersion(ctx context.Context, adminAddr string, serverCA []byte) string {
	pool, err := openbao.CertPoolFromPEM(serverCA)
	if err != nil {
		return "unknown"
	}
	h, err := openbao.New(adminAddr, "", openbao.WithCACertPool(pool)).Health(ctx)
	if err != nil {
		return "unknown"
	}
	return h.Version
}
