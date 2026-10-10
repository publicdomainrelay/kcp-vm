package vaultcert

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/netip"
	"os"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
)

func leafOfPair(t *testing.T, v VaultTLS) *x509.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(v.CertFile, v.KeyFile)
	if err != nil {
		t.Fatalf("loading the vault's TLS certificate and key: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the vault's TLS certificate: %v", err)
	}
	return leaf
}

func coversAddress(leaf *x509.Certificate, want string) bool {
	addr, err := netip.ParseAddr(want)
	if err != nil {
		return false
	}
	for _, ip := range leaf.IPAddresses {
		if parsed, err := netip.ParseAddr(ip.String()); err == nil && parsed.Unmap() == addr.Unmap() {
			return true
		}
	}
	return false
}

func TestTheVaultTLSCAOutlivesTheHostsItsCertificateCovers(t *testing.T) {
	dir := t.TempDir()
	const bringUp = "198.18.255.254"
	const gateway = "10.100.0.1"

	first, err := EnsureVaultTLSCertificate(dir, bringUp, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate at bring-up: %v", err)
	}
	if !coversAddress(leafOfPair(t, first), bringUp) {
		t.Fatalf("the vault's first certificate does not cover the address its listener was " +
			"configured for, so the bring-up material is not the one this test reasons about")
	}

	second, err := EnsureVaultTLSCertificate(dir, gateway, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate for the gateway %s: %v", gateway, err)
	}

	if !bytes.Equal(first.CAPEM, second.CAPEM) {
		t.Fatalf("the vault's TLS CA changed when a caller asked its certificate to cover one "+
			"more host. A vault process loads its certificate once, at start, and is not told to "+
			"reload it, so a client that trusts the CA written later verifies nothing the running "+
			"listener serves: every request fails with \"certificate signed by unknown authority\" "+
			"naming socialweb-vault-tls-ca, and it stays that way until the vault is restarted. "+
			"Got a new CA (%d bytes) where %d bytes were already on disk at %s",
			len(second.CAPEM), len(first.CAPEM), clusterstack.OpenBaoTLSCAPath(dir))
	}

	onDisk, err := os.ReadFile(clusterstack.OpenBaoTLSCAPath(dir))
	if err != nil {
		t.Fatalf("reading the vault's TLS CA: %v", err)
	}
	if !bytes.Equal(onDisk, second.CAPEM) {
		t.Fatal("the returned CA is not the one on disk, so a client that reads the file and a " +
			"client that uses the returned bytes trust different anchors")
	}

	pair, err := tls.LoadX509KeyPair(second.CertFile, second.KeyFile)
	if err != nil {
		t.Fatalf("loading the re-signed certificate: %v", err)
	}
	reused, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the re-signed certificate: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(onDisk) {
		t.Fatalf("the vault's TLS CA holds no CERTIFICATE PEM block")
	}
	if _, err := reused.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("the certificate now on disk does not chain to the CA now on disk: %v", err)
	}
}

func TestTheVaultTLSCertificateCarriesEveryHostItHasBeenAskedFor(t *testing.T) {
	dir := t.TempDir()
	const bringUp = "198.18.255.254"
	const gateway = "10.100.0.1"

	if _, err := EnsureVaultTLSCertificate(dir, bringUp, "127.0.0.1"); err != nil {
		t.Fatalf("EnsureVaultTLSCertificate at bring-up: %v", err)
	}
	second, err := EnsureVaultTLSCertificate(dir, gateway, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate for the gateway %s: %v", gateway, err)
	}
	leaf := leafOfPair(t, second)
	for _, want := range []string{bringUp, gateway, "127.0.0.1"} {
		if !coversAddress(leaf, want) {
			t.Fatalf("the vault's TLS certificate does not cover %s, so a caller that asked for "+
				"it would dial a listener whose certificate names a different address. IP SANs %v",
				want, leaf.IPAddresses)
		}
	}
}

func TestTheVaultTLSCAKeyIsWrittenBesideTheCAItSignsWith(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureVaultTLSCertificate(dir, "127.0.0.1"); err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
	}
	info, err := os.Stat(clusterstack.OpenBaoTLSCAKeyPath(dir))
	if err != nil {
		t.Fatalf("the vault TLS CA key is not on disk, so the CA can never sign a second "+
			"certificate and a caller that needs one more host forces a brand new trust anchor: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the vault TLS CA key at %s is mode %04o, and a key that signs the material every "+
			"client trusts is written 0600", clusterstack.OpenBaoTLSCAKeyPath(dir), info.Mode().Perm())
	}
}
