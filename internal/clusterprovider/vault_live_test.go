//go:build linux

package clusterprovider

import (
	"bytes"
	"context"
	"crypto/tls"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/identitybundle"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

func vaultSpecForStore(t *testing.T, dataDir string, port int, vaultDSN string) clusterstack.Spec {
	t.Helper()
	tlsCert, err := vaultcert.EnsureVaultTLSCertificate(dataDir, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
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

func writeAndStartBao(t *testing.T, spec clusterstack.Spec) func() {
	t.Helper()
	if err := WriteOpenBaoConfig(spec); err != nil {
		t.Fatalf("WriteOpenBaoConfig: %v", err)
	}
	argv, err := spec.OpenBaoArgv()
	if err != nil {
		t.Fatalf("OpenBaoArgv: %v", err)
	}
	logPath := filepath.Join(spec.DataDir, "vault.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", logPath, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting %v: %v", argv, err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		cancel()
		_ = logFile.Close()
	}
	t.Cleanup(stop)
	return stop
}

func startClusterVault(t *testing.T, dataDir string, port int) {
	t.Helper()
	pg, _ := startTestPostgres(t, "vaultlive")
	writeAndStartBao(t, vaultSpecForStore(t, dataDir, port, pg.VaultDSN()))
}

func TestTheClusterVaultInitialisesUnsealsAndAppliesIdempotently(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	port := freePort(t)
	startClusterVault(t, dataDir, port)

	first, err := EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("first EnsureVault: %v", err)
	}
	if first.RootToken == "" || len(first.Keys) == 0 {
		t.Fatalf("EnsureVault returned %+v, want a root token and at least one unseal key", first)
	}
	if err := ApplyClusterVault(ctx, dataDir, first); err != nil {
		t.Fatalf("first ApplyClusterVault: %v", err)
	}
	info, err := os.Stat(VaultUnsealPath(dataDir))
	if err != nil {
		t.Fatalf("the vault unseal material is not on disk: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the vault unseal material at %s is mode %04o, and the recorded policy writes it 0600",
			VaultUnsealPath(dataDir), info.Mode().Perm())
	}

	pool, err := vaultCA(dataDir)
	if err != nil {
		t.Fatalf("vaultCA: %v", err)
	}
	client := openbao.New(VaultAddr(port), first.RootToken, openbao.WithCACertPool(pool))
	health, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("Health after apply: %v", err)
	}
	if !health.Initialized || health.Sealed {
		t.Fatalf("after init, unseal and apply the vault reports initialized=%t sealed=%t", health.Initialized, health.Sealed)
	}

	admin := newVaultAdmin(VaultAddr(port), first.RootToken, pool)
	for _, mount := range []string{"secret/", "pki/"} {
		ok, err := admin.mountExists(ctx, mount)
		if err != nil {
			t.Fatalf("mountExists(%s): %v", mount, err)
		}
		if !ok {
			t.Fatalf("the applied cluster vault has no %s mount", mount)
		}
	}
	ok, err := admin.authExists(ctx, "approle")
	if err != nil {
		t.Fatalf("authExists(approle): %v", err)
	}
	if !ok {
		t.Fatal("the applied cluster vault has no approle auth method")
	}
	policy, err := client.GetPolicy(ctx, vaultlayout.PolicyIdentityBundle)
	if err != nil {
		t.Fatalf("GetPolicy(%s): %v", vaultlayout.PolicyIdentityBundle, err)
	}
	if !strings.Contains(policy, vaultlayout.PathClusterIdentity) {
		t.Fatalf("the applied policy %s does not read %s:\n%s",
			vaultlayout.PolicyIdentityBundle, vaultlayout.PathClusterIdentity, policy)
	}
	roles, err := admin.listNames(ctx, "auth/approle/role")
	if err != nil {
		t.Fatalf("listing the cluster vault's AppRoles: %v", err)
	}
	for _, want := range []string{vaultlayout.RoleClusterIdentity, vaultlayout.RoleClusterData, vaultlayout.RoleClusterNode} {
		if !slices.Contains(roles, want) {
			t.Fatalf("the applied cluster vault holds the AppRoles %v and not %s", roles, want)
		}
	}
	pkiRoles, err := admin.listNames(ctx, "pki/roles")
	if err != nil {
		t.Fatalf("listing the cluster vault's PKI roles: %v", err)
	}
	for _, want := range []string{vaultlayout.RoleKCPClient, VaultPKIRoleTenant} {
		if !slices.Contains(pkiRoles, want) {
			t.Fatalf("the applied cluster vault holds the PKI roles %v and not %s", pkiRoles, want)
		}
	}

	second, err := EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("second EnsureVault: %v", err)
	}
	if second.RootToken != first.RootToken {
		t.Fatal("the second pass replaced the root token, so the recorded unseal material and the vault disagree")
	}
	if err := ApplyClusterVault(ctx, dataDir, second); err != nil {
		t.Fatalf("second ApplyClusterVault: %v", err)
	}
	t.Logf("the cluster vault at %s initialised, unsealed and applied its layout twice without error; mounts %v; pki roles %v; approles %v",
		VaultAddr(port), []string{"secret/", "pki/"}, pkiRoles, roles)
}

func TestTheVaultIssuedTenantCredentialChainsToTheImportedClientCA(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	clientCA := newTestCA(t, "k3s-client-ca")
	serverCA := newTestCA(t, "k3s-server-ca")
	dataDir := seedClusterTLS(t, clientCA, serverCA)
	port := freePort(t)
	startClusterVault(t, dataDir, port)

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

	cfg, err := MintTenantCredentialForDataDir(ctx, dataDir, testTenantServer, CredentialSourceVaultIfSealed)
	if err != nil {
		t.Fatalf("MintTenantCredentialForDataDir from the vault: %v", err)
	}
	leaf := leafOf(t, cfg)
	if leaf.Subject.CommonName != SuperuserName {
		t.Fatalf("the vault-issued credential authenticates as %q, want %q", leaf.Subject.CommonName, SuperuserName)
	}
	if len(leaf.Subject.Organization) != 1 || leaf.Subject.Organization[0] != SuperuserOrganization {
		t.Fatalf("the vault-issued credential carries the groups %v, want [%s]", leaf.Subject.Organization, SuperuserOrganization)
	}
	verifyAgainst(t, clientCA, leaf)
	if !bytes.Equal(cfg.TLSClientConfig.CAData, serverCA.certPEM) {
		t.Fatal("the vault-issued credential does not carry the cluster's server CA")
	}
	if _, err := tls.X509KeyPair(cfg.TLSClientConfig.CertData, cfg.TLSClientConfig.KeyData); err != nil {
		t.Fatalf("the vault-issued certificate and key are not a pair: %v", err)
	}
	if cfg.BearerToken != "" {
		t.Fatal("the vault-issued credential carries a bearer token as well as a certificate")
	}
	t.Logf("the cluster vault issued the tenant credential: cn=%s org=%v serial=%s, chaining to the imported client CA",
		leaf.Subject.CommonName, leaf.Subject.Organization, leaf.SerialNumber)
}

func TestTheClusterVaultKeepsItsDataAcrossARestartOnPostgres(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	port := freePort(t)
	pg, _ := startTestPostgres(t, "vaultrestart")
	spec := vaultSpecForStore(t, dataDir, port, pg.VaultDSN())

	stop := writeAndStartBao(t, spec)
	first, err := EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("first EnsureVault: %v", err)
	}
	if err := ApplyClusterVault(ctx, dataDir, first); err != nil {
		t.Fatalf("ApplyClusterVault: %v", err)
	}
	client, err := vaultClient(dataDir, port, first.RootToken)
	if err != nil {
		t.Fatalf("vaultClient: %v", err)
	}
	if err := client.PutKV(ctx, "secret/data/restart-probe", map[string]any{"answer": "still here"}); err != nil {
		t.Fatalf("writing the probe into the vault: %v", err)
	}
	if got := sqlIn(t, pg, PostgresVaultDatabase, "select count(*) from pg_tables where schemaname='public' and tablename='openbao_kv_store'"); got != "1" {
		t.Fatalf("the vault wrote no openbao_kv_store into %s, so the rendered store and this "+
			"postgres are not the same: count=%q", PostgresVaultDatabase, got)
	}
	if got := sqlIn(t, pg, PostgresDatabase, "select coalesce(to_regclass('openbao_kv_store')::text,'absent')"); got != "absent" {
		t.Fatalf("the vault's table is in kine's database %s (%q): the vault's tables and the "+
			"objects kine writes share one database", PostgresDatabase, got)
	}

	stop()
	writeAndStartBao(t, spec)

	second, err := EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("second EnsureVault after a restart on the postgres store: %v", err)
	}
	if second.RootToken != first.RootToken {
		t.Fatal("the root token changed across the restart, so the postgres store did not hold the vault's identity")
	}
	again, err := vaultClient(dataDir, port, second.RootToken)
	if err != nil {
		t.Fatalf("vaultClient after the restart: %v", err)
	}
	got, err := again.GetKV(ctx, "secret/data/restart-probe")
	if err != nil {
		t.Fatalf("reading the probe back from the postgres store: %v", err)
	}
	if got["answer"] != "still here" {
		t.Fatalf("the probe read %v after a restart; the postgres store did not keep what was written before it", got)
	}
}

func TestAVaultPointedAtAnotherClustersStoreDoesNotAuthenticate(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()

	pgA, _ := startTestPostgres(t, "vaultcrossa")
	dirA := t.TempDir()
	portA := freePort(t)
	stopA := writeAndStartBao(t, vaultSpecForStore(t, dirA, portA, pgA.VaultDSN()))
	if _, err := EnsureVault(ctx, dirA, portA); err != nil {
		t.Fatalf("initialising cluster a's vault: %v", err)
	}
	stopA()

	dirB := t.TempDir()
	portB := freePort(t)
	if err := writeVaultSeal(dirB, VaultSeal{
		Port:      portB,
		Keys:      []string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		RootToken: "hvs.not-this-clusters-root-token",
	}); err != nil {
		t.Fatalf("writing cluster b's own unseal material: %v", err)
	}
	writeAndStartBao(t, vaultSpecForStore(t, dirB, portB, pgA.VaultDSN()))

	_, err := EnsureVault(ctx, dirB, portB)
	if err == nil {
		t.Fatal("a vault holding cluster b's unseal material opened cluster a's store: a store " +
			"reached by a cluster whose material is not its own must NOT come up")
	}
	if !strings.Contains(err.Error(), "unseal") && !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("the refusal does not name an unseal failure, so it may have failed for another "+
			"reason: %v", err)
	}
}

func logClusterVault(t *testing.T, ctx context.Context, stateDir, cluster string) {
	t.Helper()
	dataDir, err := ClusterDataDir(stateDir, cluster)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	seal, err := ReadVaultSeal(dataDir)
	if err != nil {
		t.Fatalf("the cluster vault has no unseal material after the control plane reached Ready: %v", err)
	}
	addr := VaultAddr(seal.Port)
	pool, err := vaultCA(dataDir)
	if err != nil {
		t.Fatalf("vaultCA: %v", err)
	}
	client := openbao.New(addr, seal.RootToken, openbao.WithCACertPool(pool))
	data, err := client.GetKV(ctx, vaultlayout.PathClusterIdentity)
	if err != nil {
		t.Fatalf("reading the identity bundle from the cluster vault: %v", err)
	}
	var missing []string
	for _, path := range identitybundle.Paths {
		if value, ok := data[path].(string); !ok || value == "" {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("the cluster vault at %s holds the identity bundle without %v", addr, missing)
	}
	probe := identitybundle.Paths[2]
	t.Logf("VAULT CUSTODY the cluster vault at %s holds all %d identity-bundle entries at %s; %s is present and holds %d base64 bytes",
		addr, len(identitybundle.Paths), vaultlayout.PathClusterIdentity, probe, len(data[probe].(string)))

	admin := newVaultAdmin(addr, seal.RootToken, pool)
	mounts, err := admin.list(ctx, "sys/mounts")
	if err != nil {
		t.Fatalf("listing the cluster vault's mounts: %v", err)
	}
	mountNames := make([]string, 0, len(mounts))
	for name := range mounts {
		mountNames = append(mountNames, name)
	}
	slices.Sort(mountNames)
	pkiRoles, err := admin.listNames(ctx, "pki/roles")
	if err != nil {
		t.Fatalf("listing the cluster vault's PKI roles: %v", err)
	}
	appRoles, err := admin.listNames(ctx, "auth/approle/role")
	if err != nil {
		t.Fatalf("listing the cluster vault's AppRoles: %v", err)
	}
	t.Logf("VAULT SECRETS LIST %v; PKI roles %v; AppRoles %v", mountNames, pkiRoles, appRoles)
}
