package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/rootvault"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

func requireRootVaultBao(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts a real openbao and -short was asked for: a skip is not a pass")
	if _, err := exec.LookPath("bao"); err != nil {
		livegate.RequireLocal(t, "bao is not on PATH, so the root vault's mutual TLS listener is not exercised: %v", err)
	}
}

func freeRootVaultPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("borrowing a port for the root vault: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func rootVaultFixture(t *testing.T) Config {
	t.Helper()
	stateDir := t.TempDir()
	kubeconfig := filepath.Join(stateDir, "admin.kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, ".admin-token-store"),
		[]byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("writing the shard-admin token store fixture, which kcp writes at startup: %v", err)
	}
	cfg := Config{
		Addr:          "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(freeRootVaultPort(t))),
		StateDir:      stateDir,
		UnsealPath:    filepath.Join(rootvault.Dir(stateDir), unsealFileName()),
		KCPKubeconfig: kubeconfig,
	}
	if err := prepareRoot(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("prepare-root, the entrypoint step that generates the client certificate: %v", err)
	}
	return cfg
}

func startRootVault(t *testing.T, cfg Config) {
	t.Helper()
	logPath := filepath.Join(cfg.StateDir, "root-vault.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("creating %s: %v", logPath, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "bao", "server", "-config="+rootvault.ConfigPath(cfg.StateDir))
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

	tlsConf, err := rootVaultClientTLS(cfg.StateDir)
	if err != nil {
		t.Fatalf("rootVaultClientTLS: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	last := "no answer"
	for time.Now().Before(deadline) {
		code, err := rootVaultHealth(cfg.Addr, tlsConf)
		if err == nil {
			if code != http.StatusNotImplemented {
				t.Fatalf("the mutual-TLS listener answered HTTP %d, want 501 from a vault that is "+
					"started but not yet initialised", code)
			}
			return
		}
		last = err.Error()
		time.Sleep(100 * time.Millisecond)
	}
	log, _ := os.ReadFile(logPath)
	t.Fatalf("no mutual-TLS answer at %s within 30s (last: %s). The vault log:\n%s", cfg.Addr, last, log)
}

func rootVaultHealth(addr string, tlsConf *tls.Config) (int, error) {
	hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConf}}
	resp, err := hc.Get(addr + "/v1/sys/health")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func TestTheRootVaultRefusesClientsWithNoOrAnotherCertificate(t *testing.T) {
	requireRootVaultBao(t)
	cfg := rootVaultFixture(t)
	startRootVault(t, cfg)

	material, err := vaultcert.LoadRootVaultTLS(cfg.StateDir)
	if err != nil {
		t.Fatalf("LoadRootVaultTLS: %v", err)
	}
	pool, err := material.CertPool()
	if err != nil {
		t.Fatalf("CertPool: %v", err)
	}

	code, err := rootVaultHealth(cfg.Addr, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err == nil {
		t.Fatalf("a client that presented NO certificate got HTTP %d from the root vault, so its "+
			"listener does not require one and the cross-deployment credentials ride a transport "+
			"any local process can dial", code)
	}

	other := otherCAClientCertificate(t)
	code, err = rootVaultHealth(cfg.Addr, &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{other},
		MinVersion:   tls.VersionTLS12,
	})
	if err == nil {
		t.Fatalf("a client whose certificate came from a DIFFERENT CA got HTTP %d from the root "+
			"vault, so the listener does not verify client certificates against its own CA", code)
	}

	logCode, err := rootVaultHealth(cfg.Addr, mustClientTLS(t, cfg.StateDir))
	if err != nil {
		t.Fatalf("the client certificate prepare-root generated was refused: %v", err)
	}
	if logCode != http.StatusNotImplemented {
		t.Fatalf("the generated client certificate got HTTP %d, want 501", logCode)
	}
}

func TestApplyRootPresentsTheGeneratedClientCertificateAndIsIdempotent(t *testing.T) {
	requireRootVaultBao(t)
	cfg := rootVaultFixture(t)
	startRootVault(t, cfg)

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyRoot(ctx, cfg, log); err != nil {
		t.Fatalf("the first apply-root, presenting the client certificate prepare-root generated: %v", err)
	}
	material, err := readUnsealFile(cfg.UnsealPath)
	if err != nil {
		t.Fatalf("readUnsealFile: %v", err)
	}
	firstRoot := pkiRootPEM(t, ctx, cfg, material.RootToken)

	if err := applyRoot(ctx, cfg, log); err != nil {
		t.Fatalf("the second apply-root, which must change nothing: %v", err)
	}
	secondRoot := pkiRootPEM(t, ctx, cfg, material.RootToken)
	if firstRoot != secondRoot {
		t.Fatalf("the root vault's PKI root changed between two apply-root runs, so a re-run is not " +
			"idempotent")
	}
}

func pkiRootPEM(t *testing.T, ctx context.Context, cfg Config, token string) string {
	t.Helper()
	tlsConf, err := rootVaultClientTLS(cfg.StateDir)
	if err != nil {
		t.Fatalf("rootVaultClientTLS: %v", err)
	}
	status, body, err := newAPI(cfg.Addr, token, tlsConf).raw(ctx, http.MethodGet, "pki/ca/pem", nil)
	if err != nil {
		t.Fatalf("GET pki/ca/pem: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET pki/ca/pem -> HTTP %d: %s", status, body)
	}
	return string(body)
}

func mustClientTLS(t *testing.T, stateDir string) *tls.Config {
	t.Helper()
	tlsConf, err := rootVaultClientTLS(stateDir)
	if err != nil {
		t.Fatalf("rootVaultClientTLS: %v", err)
	}
	return tlsConf
}

func otherCAClientCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the other CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "other-root-vault-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		t.Fatalf("signing the other CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing the other CA: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the other client key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "other-root-vault-client"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, key.Public(), caKey)
	if err != nil {
		t.Fatalf("signing the other client certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling the other client key: %v", err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatalf("loading the other client pair: %v", err)
	}
	return pair
}
