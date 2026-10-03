package main

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
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

func requireBaoBinary(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts a real openbao and -short was asked for: a skip is not a pass")
	if _, err := exec.LookPath("bao"); err != nil {
		livegate.RequireLocal(t, "bao is not on PATH, so the node certificate and its per-node secret are "+
			"not exercised against a real vault: %v", err)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("borrowing a port for the test vault: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startVaultPostgres(t *testing.T) *clusterprovider.Postgres {
	t.Helper()
	for _, bin := range []string{"initdb", "postgres", "psql"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH: the vault's store is this cluster's own embedded-postgres, so there is no vault to start", bin)
		}
	}
	pg, err := clusterprovider.StartPostgres(context.Background(), clusterprovider.PostgresSpec{
		Cluster:  "vaultnode",
		StateDir: t.TempDir(),
		Port:     freeTCPPort(t),
		Log:      testLogger(),
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

func writeTestClientCA(t *testing.T, dataDir string) {
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
		Subject:               pkix.Name{CommonName: "socialweb-node-test-client-ca"},
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
	dir := clusterstack.TLSDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"client-ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})},
		{"client-ca.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", f.name, err)
		}
	}
}

func startTLSVault(t *testing.T, dataDir, gateway string, port int) {
	t.Helper()
	tlsCert, err := vaultcert.EnsureVaultTLSCertificate(dataDir, gateway, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
	}
	spec := clusterstack.Spec{
		DataDir:            dataDir,
		OpenBaoConfig:      clusterstack.OpenBaoConfigPath(dataDir),
		OpenBaoPort:        port,
		OpenBaoTLSCertFile: tlsCert.CertFile,
		OpenBaoTLSKeyFile:  tlsCert.KeyFile,
		OpenBaoPostgresDSN: startVaultPostgres(t).VaultDSN(),
	}
	if err := clusterprovider.WriteOpenBaoConfig(spec); err != nil {
		t.Fatalf("WriteOpenBaoConfig: %v", err)
	}
	logFile, err := os.Create(filepath.Join(dataDir, "vault.log"))
	if err != nil {
		t.Fatalf("creating the vault log: %v", err)
	}
	argv, err := spec.OpenBaoArgv()
	if err != nil {
		t.Fatalf("OpenBaoArgv: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
	forwardAlias(t, gateway, port)
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

func TestTheNodeVaultStageMintsACertificateItCanFetchItsOwnSecretWith(t *testing.T) {
	requireBaoBinary(t)
	ctx := context.Background()
	const gateway = "127.0.0.2"
	const cluster = "liveweb"
	const node = "liveweb-node1"
	stateDir := t.TempDir()
	dataDir, err := clusterprovider.ClusterDataDir(stateDir, cluster)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	writeTestClientCA(t, dataDir)
	port := freeTCPPort(t)
	startTLSVault(t, dataDir, gateway, port)

	seal, err := clusterprovider.EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("EnsureVault: %v", err)
	}
	if err := clusterprovider.ApplyClusterVault(ctx, dataDir, seal); err != nil {
		t.Fatalf("ApplyClusterVault: %v", err)
	}
	if err := clusterprovider.ImportClientCA(ctx, dataDir, seal); err != nil {
		t.Fatalf("ImportClientCA: %v", err)
	}

	substrate := nodeSubstrate{Name: SubstrateFirecracker, Network: nodefirecracker.Network{Gateway: gateway}}
	stage := nodeVaultStage(substrate, cluster, dataDir, node)
	fill := t.TempDir()
	if err := stage(ctx, fill, "np-node1-secret"); err != nil {
		t.Fatalf("nodeVaultStage: %v", err)
	}

	cert, err := vaultcert.Load(fill)
	if err != nil {
		t.Fatalf("the stage wrote a certificate the node cannot Load back: %v", err)
	}
	wantURL := "https://" + net.JoinHostPort(gateway, strconv.Itoa(port))
	if cert.Metadata.VaultAddr != wantURL {
		t.Fatalf("the node certificate dials %q, want the tenant-facing vault URL %q",
			cert.Metadata.VaultAddr, wantURL)
	}
	own, err := cert.FetchOwnSecret(ctx)
	if err != nil {
		t.Fatalf("the node could not fetch its own secret over the vault's TLS listener: %v", err)
	}
	if own["password"] != "np-node1-secret" {
		t.Fatalf("the node read %v, want the per-node password the provider produced", own)
	}

	if _, err := cert.ReadKV(ctx, vaultlayout.NodeSecretPath("liveweb-node2")); !errors.Is(err, openbao.ErrForbidden) {
		t.Fatalf("the node certificate read another node's path: got %v, want ErrForbidden", err)
	}
}

func TestTheDiskBuildRunsTheNodeStage(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, nodefirecracker.NodeDiskName)
	sentinel := []byte("KCP_DISK_STAGE_RAN")
	doc := nodefirecracker.NodeDocument{
		NodeName:     "liveweb-node1",
		Server:       "https://10.100.0.1:24000",
		Token:        "K10secret",
		NodePassword: "np-node1-secret",
		Address:      "10.100.0.2",
		Prefix:       24,
		Gateway:      "10.100.0.1",
		Interface:    "eth0",
	}
	stage := func(fill string) error {
		return os.WriteFile(filepath.Join(fill, "stage.sentinel"), sentinel, 0o600)
	}
	if err := nodefirecracker.BuildNodeDisk(disk, doc, nodefirecracker.DefaultDiskMiB, stage); err != nil {
		t.Fatalf("BuildNodeDisk: %v", err)
	}
	if _, err := exec.LookPath("debugfs"); err != nil {
		livegate.RequireLocal(t, "debugfs is not on this host, so the disk cannot be read back to prove "+
			"the node stage ran inside the image build: %v", err)
	}
	out, err := exec.Command("debugfs", "-R", "cat /stage.sentinel", disk).Output()
	if err != nil {
		t.Fatalf("reading the staged file back from the disk: %v", err)
	}
	if string(out) != string(sentinel) {
		t.Fatalf("the disk carries %q at /stage.sentinel, want %q: the node stage did not run "+
			"before mkfs", out, sentinel)
	}
}
