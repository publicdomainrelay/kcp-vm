package vaultcert

import (
	"bytes"
	"crypto/x509"
	"net/netip"
	"os"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/rootvault"
)

const rootVaultTestHost = "socialweb-rootvault-test"

func TestRootVaultTLSIsStableAcrossRunsAndCoversItsHosts(t *testing.T) {
	stateDir := t.TempDir()
	hosts := []string{rootvault.LoopbackHost, rootVaultTestHost}

	first, err := EnsureRootVaultTLS(stateDir, hosts...)
	if err != nil {
		t.Fatalf("EnsureRootVaultTLS: %v", err)
	}
	second, err := EnsureRootVaultTLS(stateDir, hosts...)
	if err != nil {
		t.Fatalf("EnsureRootVaultTLS on the material that already exists: %v", err)
	}

	if !bytes.Equal(first.CAPEM, second.CAPEM) {
		t.Fatalf("the root vault's CA changed between two runs, so a client that trusted the first " +
			"one is refused by the second")
	}
	for _, pair := range []struct {
		name string
		path string
	}{
		{"server certificate", first.ServerCertFile},
		{"client certificate", first.ClientCertFile},
	} {
		before, err := os.ReadFile(pair.path)
		if err != nil {
			t.Fatalf("reading %s: %v", pair.path, err)
		}
		after, err := os.ReadFile(pair.path)
		if err != nil {
			t.Fatalf("reading %s again: %v", pair.path, err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("the root vault's %s changed between two runs, so the vault's identity is not "+
				"stable under it", pair.name)
		}
	}

	pool, err := first.CertPool()
	if err != nil {
		t.Fatalf("CertPool: %v", err)
	}
	serverLeaf, err := parseLeaf(mustReadFile(t, first.ServerCertFile))
	if err != nil {
		t.Fatalf("parsing the server certificate: %v", err)
	}
	if _, err := serverLeaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("the server certificate does not chain to the root vault's own CA: %v", err)
	}
	assertCoversHosts(t, serverLeaf, hosts)

	clientLeaf, err := parseLeaf(mustReadFile(t, first.ClientCertFile))
	if err != nil {
		t.Fatalf("parsing the client certificate: %v", err)
	}
	if _, err := clientLeaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("the client certificate does not chain to the root vault's own CA for client auth: %v", err)
	}
	if _, err := serverLeaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err == nil {
		t.Fatalf("the server certificate also verifies for client auth, so the two roles are not " +
			"separated by extended key usage")
	}
}

func TestRootVaultTLSRotatesWhenAHostItMustCoverIsGone(t *testing.T) {
	stateDir := t.TempDir()
	first, err := EnsureRootVaultTLS(stateDir, rootvault.LoopbackHost, rootVaultTestHost)
	if err != nil {
		t.Fatalf("EnsureRootVaultTLS: %v", err)
	}
	second, err := EnsureRootVaultTLS(stateDir, rootvault.LoopbackHost, "another-host")
	if err != nil {
		t.Fatalf("EnsureRootVaultTLS with a different host: %v", err)
	}
	if bytes.Equal(first.CAPEM, second.CAPEM) {
		t.Fatalf("the material was reused even though the server certificate did not cover the " +
			"host that is now dialled")
	}
	leaf, err := parseLeaf(mustReadFile(t, second.ServerCertFile))
	if err != nil {
		t.Fatalf("parsing the regenerated server certificate: %v", err)
	}
	assertCoversHosts(t, leaf, []string{rootvault.LoopbackHost, "another-host"})
}

func TestRootVaultTLSFilesArePrivate(t *testing.T) {
	stateDir := t.TempDir()
	material, err := EnsureRootVaultTLS(stateDir, rootvault.LoopbackHost)
	if err != nil {
		t.Fatalf("EnsureRootVaultTLS: %v", err)
	}
	for _, path := range []string{
		material.CAPath,
		material.ServerCertFile,
		material.ServerKeyFile,
		material.ClientCertFile,
		material.ClientKeyFile,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s is mode %04o, want 0600: the material is on the same volume as the vault "+
				"it protects", path, info.Mode().Perm())
		}
	}
	info, err := os.Stat(rootvault.TLSDir(stateDir))
	if err != nil {
		t.Fatalf("stat %s: %v", rootvault.TLSDir(stateDir), err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("the TLS directory is mode %04o, want 0700", info.Mode().Perm())
	}
}

func TestLoadRootVaultTLSRefusesAMissingCA(t *testing.T) {
	if _, err := LoadRootVaultTLS(t.TempDir()); err == nil {
		t.Fatalf("LoadRootVaultTLS accepted a state dir holding no CA, so apply-root would dial the " +
			"root vault trusting no authority")
	}
}

func assertCoversHosts(t *testing.T, leaf *x509.Certificate, hosts []string) {
	t.Helper()
	for _, host := range hosts {
		found := false
		for _, name := range leaf.DNSNames {
			if name == host {
				found = true
			}
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			for _, ip := range leaf.IPAddresses {
				if ip.Equal(addr.AsSlice()) {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("the root vault's server certificate does not name %s, so the client that "+
				"dials it fails verification: DNS %v IP %v", host, leaf.DNSNames, leaf.IPAddresses)
		}
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return raw
}
