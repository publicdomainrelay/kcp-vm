//go:build linux

package clusterprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

const tenantAddOnGateway = "10.100.0.1"

func vaultCAFingerprint(t *testing.T, dataDir string) string {
	t.Helper()
	raw, err := os.ReadFile(clusterstack.OpenBaoTLSCAPath(dataDir))
	if err != nil {
		t.Fatalf("reading the cluster vault's TLS CA: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func vaultSpecForHosts(t *testing.T, dataDir string, port int, vaultDSN string, hosts ...string) clusterstack.Spec {
	t.Helper()
	tlsCert, err := vaultcert.EnsureVaultTLSCertificate(dataDir, hosts...)
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate%v: %v", hosts, err)
	}
	return clusterstack.Spec{
		DataDir:            dataDir,
		OpenBaoConfig:      clusterstack.OpenBaoConfigPath(dataDir),
		OpenBaoPort:        port,
		OpenBaoTLSCertFile: tlsCert.CertFile,
		OpenBaoTLSKeyFile:  tlsCert.KeyFile,
		OpenBaoPostgresDSN: vaultDSN,
	}
}

func TestTheVaultStillMintsAfterTheTenantAddOnPassAsksForAnotherHost(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	dataDir := seedClusterTLS(t, clientCA, serverCA)
	port := freePort(t)

	pg, _ := startTestPostgres(t, "vaultgrow")
	spec := vaultSpecForHosts(t, dataDir, port, pg.VaultDSN(),
		portallocator.MarketAdvertiseAddress, "127.0.0.1")
	stop := writeAndStartBao(t, spec)

	seal, err := EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("EnsureVault: %v", err)
	}
	if err := ApplyClusterVault(ctx, dataDir, seal); err != nil {
		t.Fatalf("ApplyClusterVault: %v", err)
	}
	if err := ImportClientCA(ctx, dataDir, seal); err != nil {
		t.Fatalf("ImportClientCA: %v", err)
	}

	before := vaultCAFingerprint(t, dataDir)

	tlsCert, err := vaultcert.EnsureVaultTLSCertificate(dataDir, tenantAddOnGateway, "127.0.0.1")
	if err != nil {
		t.Fatalf("the tenant add-on pass could not ask the vault's certificate to cover %s: %v",
			tenantAddOnGateway, err)
	}
	after := vaultCAFingerprint(t, dataDir)

	cfg, err := MintTenantCredentialForDataDir(ctx, dataDir, testTenantServer)
	if err != nil {
		t.Fatalf("the vault's TLS CA on disk is %s before the tenant add-on pass asked for %s and "+
			"%s after it, while the vault that is LISTENING on %s loaded its certificate once, at "+
			"start, and was never told to reload: the mint has to keep working because the vault's "+
			"trust anchor is the certificate the listener is serving right now. Got: %v",
			before, tenantAddOnGateway, after, VaultAddr(port), err)
	}
	leaf := leafOf(t, cfg)
	if leaf.Subject.CommonName != SuperuserName {
		t.Fatalf("the credential minted after the host set grew authenticates as %q, want %q",
			leaf.Subject.CommonName, SuperuserName)
	}
	if before != after {
		t.Fatalf("the vault's TLS CA changed from %s to %s when a caller asked its certificate to "+
			"cover one more host: every certificate the listener already loaded was signed by the "+
			"old CA, so the client's pool verifies nothing the vault serves", before, after)
	}

	stop()
	writeAndStartBao(t, spec)
	if _, err := EnsureVault(ctx, dataDir, port); err != nil {
		t.Fatalf("unsealing the vault after a restart: %v", err)
	}
	if _, err := MintTenantCredentialForDataDir(ctx, dataDir, testTenantServer); err != nil {
		t.Fatalf("the mint failed after the vault was restarted onto the certificate the tenant "+
			"add-on pass left on disk, so a cluster whose anchor had already moved under its "+
			"listener cannot be repaired by restarting it: %v", err)
	}
	t.Logf("the vault's TLS CA stayed %s across the tenant add-on pass and across a restart; the "+
		"certificate on disk is %s; the vault at %s issued %s chaining to the imported client CA",
		after, tlsCert.CertFile, VaultAddr(port), leaf.Subject.CommonName)
}
