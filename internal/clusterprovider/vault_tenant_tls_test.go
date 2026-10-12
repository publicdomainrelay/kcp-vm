//go:build linux

package clusterprovider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

func testVaultTLS() func(string, ...string) (VaultTLS, error) {
	return func(dataDir string, hosts ...string) (VaultTLS, error) {
		tls, err := vaultcert.EnsureVaultTLSCertificate(dataDir, hosts...)
		if err != nil {
			return VaultTLS{}, err
		}
		return VaultTLS{CertFile: tls.CertFile, KeyFile: tls.KeyFile, CAPEM: tls.CAPEM}, nil
	}
}

func marketVaultStackConfig(t *testing.T) StackConfig {
	t.Helper()
	cfg := stackConfig(t, fakeAdmin())
	cfg.Stack = DesignedStack
	cfg.AdvertiseAddress = portallocator.MarketAdvertiseAddress
	cfg.MarketDialled = true
	cfg.VaultTLS = testVaultTLS()
	cfg.VaultStoreDSN = func(string) (string, error) { return fakeVaultStoreDSN(), nil }
	return cfg
}

func TestTheClustersVaultRendersOneLoopbackTLSListener(t *testing.T) {
	cfg := marketVaultStackConfig(t)
	leases := designedLeases(21003)
	steps, err := StepsFor(cfg)(observed(), leases)
	if err != nil {
		t.Fatalf("StepsFor: %v", err)
	}
	if len(steps) == 0 {
		t.Fatal("StepsFor built no steps")
	}
	dataDir, err := ClusterDataDir(cfg.StateDir, "liveweb")
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	path := clusterstack.OpenBaoConfigPath(dataDir)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the wired stack left no vault config at %s: %v", path, err)
	}
	text := string(body)
	if got := strings.Count(text, `listener "tcp"`); got != 1 {
		t.Fatalf("the vault config names %d listeners, want exactly 1: the vault listens on "+
			"loopback with TLS, and a market node reaches it through the market's door at the "+
			"advertised address, which is a forward and not a second bind:\n%s", got, text)
	}
	port := leases[portallocator.PurposeOpenBao].Port
	loopback := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for _, want := range []string{
		loopback,
		`tls_cert_file = ` + strconv.Quote(clusterstack.OpenBaoTLSCertPath(dataDir)),
		`tls_key_file  = ` + strconv.Quote(clusterstack.OpenBaoTLSKeyPath(dataDir)),
		`api_addr = "https://` + loopback + `"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the wired vault config does not carry %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tls_disable") {
		t.Fatalf("the config names tls_disable, so a plaintext listener is back:\n%s", text)
	}
	if _, err := os.Stat(clusterstack.OpenBaoTLSCertPath(dataDir)); err != nil {
		t.Fatalf("the config names a TLS certificate that was never written: %v", err)
	}
	if _, err := os.Stat(clusterstack.OpenBaoTLSCAPath(dataDir)); err != nil {
		t.Fatalf("the config names a TLS certificate whose CA was never written: %v", err)
	}

	pair, err := tls.LoadX509KeyPair(clusterstack.OpenBaoTLSCertPath(dataDir), clusterstack.OpenBaoTLSKeyPath(dataDir))
	if err != nil {
		t.Fatalf("loading the vault's TLS certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the vault's TLS certificate: %v", err)
	}
	for _, want := range []string{"127.0.0.1", portallocator.MarketAdvertiseAddress} {
		found := false
		for _, name := range leaf.DNSNames {
			if name == want {
				found = true
			}
		}
		for _, ip := range leaf.IPAddresses {
			if ip.String() == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("the vault's TLS certificate does not name %s, so a node that dials the "+
				"advertised address or the admin that dials loopback fails verification: DNS %v IP %v",
				want, leaf.DNSNames, leaf.IPAddresses)
		}
	}
}

func TestTheClustersVaultAnswersOnLoopbackWithTLS(t *testing.T) {
	skipWithoutBao(t)
	cfg := marketVaultStackConfig(t)
	pg, _ := startTestPostgres(t, "vaulttls")
	cfg.VaultStoreDSN = func(string) (string, error) { return pg.VaultDSN(), nil }
	leases := designedLeases(21003)
	if _, err := StepsFor(cfg)(observed(), leases); err != nil {
		t.Fatalf("StepsFor: %v", err)
	}
	dataDir, err := ClusterDataDir(cfg.StateDir, "liveweb")
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	port := leases[portallocator.PurposeOpenBao].Port
	spec := clusterstack.Spec{
		DataDir:       dataDir,
		OpenBaoConfig: clusterstack.OpenBaoConfigPath(dataDir),
		OpenBaoPort:   port,
	}
	argv, err := spec.OpenBaoArgv()
	if err != nil {
		t.Fatalf("OpenBaoArgv: %v", err)
	}
	logPath := filepath.Join(dataDir, "vault-tls.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create %s: %v", logPath, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %v: %v", argv, err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		_ = logFile.Close()
	})

	caPEM, err := os.ReadFile(clusterstack.OpenBaoTLSCAPath(dataDir))
	if err != nil {
		t.Fatalf("reading the vault TLS CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("the vault TLS CA holds no CERTIFICATE PEM block")
	}
	loopbackCode := waitHealth(t, "https://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, logPath)
	if loopbackCode != http.StatusNotImplemented {
		t.Fatalf("the loopback listener answered HTTP %d, want 501 over verified TLS", loopbackCode)
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort(portallocator.MarketAdvertiseAddress, strconv.Itoa(port)), time.Second); err == nil {
		t.Fatalf("the vault accepts a connection on %s: a listener outside loopback is what needed "+
			"root, and a node reaches the vault through the market's door, not a second bind",
			portallocator.MarketAdvertiseAddress)
	}
}

func waitHealth(t *testing.T, url string, tlsConfig *tls.Config, logPath string) int {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	if tlsConfig != nil {
		client.Transport = &http.Transport{TLSClientConfig: tlsConfig}
	}
	deadline := time.Now().Add(30 * time.Second)
	last := "no answer"
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/v1/sys/health")
		if err == nil {
			defer resp.Body.Close()
			return resp.StatusCode
		}
		last = err.Error()
		time.Sleep(PollInterval)
	}
	log, _ := os.ReadFile(logPath)
	t.Fatalf("no vault listener answered at %s (last: %s). The vault log:\n%s", url, last, log)
	return 0
}

func TestTheClustersVaultRefusesToRenderWithoutACertificateSource(t *testing.T) {
	cfg := stackConfig(t, fakeAdmin())
	cfg.Stack = DesignedStack
	cfg.AdvertiseAddress = portallocator.MarketAdvertiseAddress
	cfg.MarketDialled = true
	if _, err := StepsFor(cfg)(observed(), designedLeases(21003)); err == nil {
		t.Fatal("a vault rendered with no TLS certificate source, so its single listener would " +
			"come up without a certificate and auth/cert could never answer on it")
	}
}
