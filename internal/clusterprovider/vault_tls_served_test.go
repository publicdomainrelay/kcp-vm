//go:build linux

package clusterprovider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

func clientNameMismatch(t *testing.T, addr, serverName string, caPEM []byte) string {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("the vault's TLS CA on disk holds no certificate PEM block")
	}
	raw, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dialing the vault at %s: %v", addr, err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12})
	err = conn.Handshake()
	if err == nil {
		t.Fatalf("a client dialing the vault at %s as %s verified the handshake against the CA on "+
			"disk, while the leaf the vault is serving does not carry %s: the reading this test "+
			"exists to take did not reproduce", addr, serverName, serverName)
	}
	return err.Error()
}

func leafHostsOf(t *testing.T, leaf *x509.Certificate) []string {
	t.Helper()
	out := append([]string(nil), leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

func servedLeafHosts(t *testing.T, addr string) []string {
	t.Helper()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	raw, err := dialer.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dialing the vault at %s: %v", addr, err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err := conn.Handshake(); err != nil {
		t.Fatalf("the TLS handshake with %s: %v", addr, err)
	}
	served := conn.ConnectionState().PeerCertificates
	if len(served) == 0 {
		t.Fatalf("the vault at %s served no certificate", addr)
	}
	return leafHostsOf(t, served[0])
}

func diskLeafHosts(t *testing.T, certFile, keyFile string) []string {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("loading the leaf on disk at %s: %v", certFile, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the leaf on disk at %s: %v", certFile, err)
	}
	return leafHostsOf(t, leaf)
}

func awaitServedLeafCovers(t *testing.T, addr, host string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		last = vaultcert.RequireServedVaultTLS(ctx, addr, host)
		if last == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the vault at %s never served a certificate carrying %s: %v", addr, host, last)
}

func TestARunningVaultDoesNotServeAHostItsLeafGainedAfterItStarted(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	dataDir := seedClusterTLS(t, clientCA, serverCA)
	port := freePort(t)
	pg, _ := startTestPostgres(t, "vaultserved")
	spec := vaultSpecForHosts(t, dataDir, port, pg.VaultDSN(),
		portallocator.MarketAdvertiseAddress, "127.0.0.1")
	writeAndStartBao(t, spec)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	awaitServedLeafCovers(t, addr, portallocator.MarketAdvertiseAddress)

	if _, err := vaultcert.EnsureVaultTLSCertificate(dataDir, tenantAddOnGateway, "127.0.0.1"); err != nil {
		t.Fatalf("the tenant add-on pass could not ask the vault's certificate to cover %s: %v",
			tenantAddOnGateway, err)
	}

	disk := diskLeafHosts(t, spec.OpenBaoTLSCertFile, spec.OpenBaoTLSKeyFile)
	served := servedLeafHosts(t, addr)
	caPEM, err := os.ReadFile(clusterstack.OpenBaoTLSCAPath(dataDir))
	if err != nil {
		t.Fatalf("reading the vault's TLS CA on disk: %v", err)
	}
	mismatch := clientNameMismatch(t, addr, tenantAddOnGateway, caPEM)
	t.Logf("reading: the vault at %s started on a leaf covering [%s 127.0.0.1]; the tenant add-on "+
		"pass then asked its certificate to cover %s, and the leaf now on disk covers %v while the "+
		"leaf the vault is serving covers %v. A client dialing %s as %s fails: %s",
		addr, portallocator.MarketAdvertiseAddress, tenantAddOnGateway, disk, served, addr,
		tenantAddOnGateway, mismatch)

	if !strings.Contains(strings.Join(disk, " "), tenantAddOnGateway) {
		t.Fatalf("the leaf on disk does not carry %s, so this test is not reproducing the tenant "+
			"add-on pass's re-sign: %v", tenantAddOnGateway, disk)
	}
	if strings.Contains(strings.Join(served, " "), tenantAddOnGateway) {
		t.Fatalf("the running vault is already serving %s, so the window this test is about did "+
			"not open: the served leaf carries %v", tenantAddOnGateway, served)
	}

	err = vaultcert.RequireServedVaultTLS(ctx, addr, tenantAddOnGateway)
	if err == nil {
		t.Fatalf("the vault running at %s was taken to be serving %s, while the leaf it presents "+
			"carries only %v and the leaf on disk carries %v: a client handed a URL on %s would "+
			"verify nothing until the vault restarts",
			addr, tenantAddOnGateway, served, disk, tenantAddOnGateway)
	}
	if !strings.Contains(err.Error(), tenantAddOnGateway) {
		t.Fatalf("the refusal does not name the host the running listener cannot present: %v", err)
	}
	if err := vaultcert.RequireServedVaultTLS(ctx, addr, portallocator.MarketAdvertiseAddress); err != nil {
		t.Fatalf("the running vault stopped serving the host it started with, so the check refuses "+
			"more than the host the listener lacks: %v", err)
	}
}
