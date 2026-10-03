//go:build linux

package clusterprovider

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

func skipWithoutBao(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts a real openbao and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	if _, err := exec.LookPath("bao"); err != nil {
		livegate.RequireLocal(t, "bao is not on PATH: there is no vault binary to start, so there is nothing to "+
			"exercise")
	}
}

func fakeVaultStoreDSN() string {
	return "postgres://postgres:secret@127.0.0.1:21001/" + clusterstack.OpenBaoDatabase + "?sslmode=disable"
}

func openBaoSpec(t *testing.T) clusterstack.Spec {
	t.Helper()
	dir := t.TempDir()
	tls, err := vaultcert.EnsureVaultTLSCertificate(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
	}
	return clusterstack.Spec{
		DataDir:            dir,
		OpenBaoConfig:      clusterstack.OpenBaoConfigPath(dir),
		OpenBaoPort:        freePort(t),
		OpenBaoTLSCertFile: tls.CertFile,
		OpenBaoTLSKeyFile:  tls.KeyFile,
		OpenBaoPostgresDSN: fakeVaultStoreDSN(),
	}
}

func TestTheConfigIsWrittenWhereTheArgvReadsIt(t *testing.T) {
	s := openBaoSpec(t)
	if err := WriteOpenBaoConfig(s); err != nil {
		t.Fatalf("WriteOpenBaoConfig: %v", err)
	}

	argv, err := s.OpenBaoArgv()
	if err != nil {
		t.Fatalf("OpenBaoArgv: %v", err)
	}
	want := "-config=" + s.OpenBaoConfig
	if len(argv) != 3 || argv[2] != want {
		t.Fatalf("the vault's argv is %v and the writer wrote %s: the argv names the file bao "+
			"reads, so a writer that puts one somewhere else leaves bao failing at start with a "+
			"cause that is not in its log", argv, s.OpenBaoConfig)
	}

	info, err := os.Stat(s.OpenBaoConfig)
	if err != nil {
		t.Fatalf("stat %s: %v", s.OpenBaoConfig, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s is mode %04o: it can name a storage path and a listener, and it is the one "+
			"file a reader of this cluster's dir would use to reason about its vault",
			s.OpenBaoConfig, info.Mode().Perm())
	}

	body, err := os.ReadFile(s.OpenBaoConfig)
	if err != nil {
		t.Fatalf("read %s: %v", s.OpenBaoConfig, err)
	}
	rendered, err := s.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig: %v", err)
	}
	if string(body) != rendered {
		t.Fatalf("the file on disk is not what the renderer produces:\n%s", body)
	}
}

func TestWritingTheConfigTwiceChangesNothing(t *testing.T) {
	s := openBaoSpec(t)
	if err := WriteOpenBaoConfig(s); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.Stat(s.OpenBaoConfig)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := WriteOpenBaoConfig(s); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.Stat(s.OpenBaoConfig)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !os.SameFile(first, second) {
		t.Fatal("the second write replaced the file. Every reconcile pass writes, so a rewrite " +
			"per pass is a new inode per pass: a vault reading it and a reader watching it see the " +
			"file appear to change when nothing about the vault did")
	}
	if second.Mode().Perm() != 0o600 {
		t.Fatalf("the second write left mode %04o", second.Mode().Perm())
	}

	rendered, err := s.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig: %v", err)
	}
	if err := os.WriteFile(s.OpenBaoConfig, []byte("ui = true\n"), 0o644); err != nil {
		t.Fatalf("tampering: %v", err)
	}
	if err := WriteOpenBaoConfig(s); err != nil {
		t.Fatalf("write over the tampered file: %v", err)
	}
	info, err := os.Stat(s.OpenBaoConfig)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("a file rewritten after tampering kept mode %04o: the mode check is part of "+
			"idempotence, not decoration", info.Mode().Perm())
	}
	body, err := os.ReadFile(s.OpenBaoConfig)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != rendered {
		t.Fatalf("the tampered file was not restored:\n%s", body)
	}
}

func TestTheConfigIsRefusedWhenTheVaultHasNoLeasedPort(t *testing.T) {
	s := openBaoSpec(t)
	s.OpenBaoPort = 0
	if err := WriteOpenBaoConfig(s); !errors.Is(err, clusterstack.ErrNoOpenBaoPort) {
		t.Fatalf("a vault with no leased port was written: %v", err)
	}
	if _, err := os.Stat(s.OpenBaoConfig); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused write left %s on disk: err=%v", s.OpenBaoConfig, err)
	}
}

func designedLeases(apiserver int) materialiser.Leases {
	return materialiser.Leases{
		portallocator.PurposePostgres:                  {Port: 21001, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposePostgres},
		portallocator.PurposeOpenBao:                   {Port: 21004, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeOpenBao},
		portallocator.PurposeKine:                      {Port: 21002, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeKine},
		portallocator.PurposeK3sSecure:                 {Port: apiserver, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sSecure},
		portallocator.PurposeK3sControllerManager:      {Port: 21009, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sControllerManager},
		portallocator.PurposeK3sScheduler:              {Port: 21010, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sScheduler},
		portallocator.PurposeK3sCloudControllerManager: {Port: 21011, Width: 1, Cluster: "liveweb", Purpose: portallocator.PurposeK3sCloudControllerManager},
	}
}

func fakeAdmin() AdminClient {
	return func(context.Context, string, string) (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	}
}

func TestTheDesignedStackPutsTheConfigOnDiskBeforeTheVaultStep(t *testing.T) {
	stateDir := t.TempDir()
	cfg := StackConfig{
		Stack:            DesignedStack,
		Admin:            fakeAdmin(),
		StateDir:         stateDir,
		StoreEndpoint:    func(string) (string, error) { return "postgres://postgres@127.0.0.1:21001/k3s?sslmode=disable", nil },
		VaultStoreDSN:    func(string) (string, error) { return fakeVaultStoreDSN(), nil },
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      "0.0.0.0",
		VaultTLS:         testVaultTLS(),
		AuditPolicyPath:  "/tmp/audit-policy.yaml",
	}
	leases := designedLeases(21003)

	steps, err := StepsFor(cfg)(observed(), leases)
	if err != nil {
		t.Fatalf("StepsFor(DesignedStack): %v", err)
	}

	idx := -1
	for i, s := range steps {
		if s.Name == procgroup.ComponentOpenBao {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("the designed stack built no vault step: %v", names(steps))
	}

	dataDir, err := ClusterDataDir(stateDir, "liveweb")
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	path := clusterstack.OpenBaoConfigPath(dataDir)
	argv := steps[idx].Spec.Argv
	if len(argv) != 3 || argv[2] != "-config="+path {
		t.Fatalf("the vault step's argv is %v, and the lease is %d: the config it reads must be "+
			"the one this cluster's data dir holds", argv, leases[portallocator.PurposeOpenBao].Port)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the stack built a vault step that reads %s and nothing wrote it: %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s is mode %04o", path, info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	want := fmt.Sprintf("127.0.0.1:%d", leases[portallocator.PurposeOpenBao].Port)
	if !strings.Contains(string(body), want) {
		t.Fatalf("the written config does not listen on the port the cluster leased (%s):\n%s",
			want, body)
	}
	if !strings.Contains(string(body), `storage "postgresql" {`) {
		t.Fatalf("the written config does not use the postgres store:\n%s", body)
	}
	if !strings.Contains(string(body), fakeVaultStoreDSN()) {
		t.Fatalf("the written config does not carry this cluster's vault DSN:\n%s", body)
	}
}

func TestAVaultedStackWithoutAStoreDSNIsRefused(t *testing.T) {
	stateDir := t.TempDir()
	cfg := StackConfig{
		Stack:            DesignedStack,
		Admin:            fakeAdmin(),
		StateDir:         stateDir,
		StoreEndpoint:    func(string) (string, error) { return "postgres://postgres@127.0.0.1:21001/k3s?sslmode=disable", nil },
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      "0.0.0.0",
		VaultTLS:         testVaultTLS(),
		AuditPolicyPath:  "/tmp/audit-policy.yaml",
	}
	if _, err := StepsFor(cfg)(observed(), designedLeases(21003)); !errors.Is(err, ErrNoVaultStore) {
		t.Fatalf("a stack with a vault and no store DSN was accepted: %v", err)
	}
}

func TestTheProvenStackWritesNoVaultConfig(t *testing.T) {
	stateDir := t.TempDir()
	cfg := StackConfig{
		Stack:            ProvenStack,
		Admin:            fakeAdmin(),
		StateDir:         stateDir,
		StoreEndpoint:    func(string) (string, error) { return "postgres://postgres@127.0.0.1:21001/k3s?sslmode=disable", nil },
		AdvertiseAddress: portallocator.LoopbackAdvertiseAddress,
		BindAddress:      "0.0.0.0",
		AuditPolicyPath:  "/tmp/audit-policy.yaml",
	}
	if _, err := StepsFor(cfg)(observed(), stackLeases(21003)); err != nil {
		t.Fatalf("StepsFor(ProvenStack): %v", err)
	}
	dataDir, err := ClusterDataDir(stateDir, "liveweb")
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	if _, err := os.Stat(clusterstack.OpenBaoConfigPath(dataDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a stack that runs no vault left a vault config on disk: err=%v. The lease for "+
			"the vault port is taken only when the vault is in the Stack (PurposesFor); the config "+
			"is the other half of the same rule", err)
	}
}

func waitForVault(t *testing.T, dataDir string, port int, logPath string) (int, string) {
	t.Helper()
	pool, err := vaultCA(dataDir)
	if err != nil {
		t.Fatalf("vaultCA: %v", err)
	}
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	url := fmt.Sprintf("%s/v1/sys/health", VaultAddr(port))
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				return resp.StatusCode, string(body)
			}
			last = err
		} else {
			last = err
		}
		time.Sleep(PollInterval)
	}
	tail, _ := os.ReadFile(logPath)
	t.Fatalf("the vault never answered on the leased port %d (last dial: %v). Its log:\n%s",
		port, last, tail)
	return 0, ""
}

func TestARealVaultReadsTheRenderedConfigAndAnswersOnTheLeasedPort(t *testing.T) {
	skipWithoutBao(t)
	s := openBaoSpec(t)
	pg, _ := startTestPostgres(t, "vaultstore")
	s.OpenBaoPostgresDSN = pg.VaultDSN()
	if err := WriteOpenBaoConfig(s); err != nil {
		t.Fatalf("WriteOpenBaoConfig: %v", err)
	}
	argv, err := s.OpenBaoArgv()
	if err != nil {
		t.Fatalf("OpenBaoArgv: %v", err)
	}
	config, err := os.ReadFile(s.OpenBaoConfig)
	if err != nil {
		t.Fatalf("read %s: %v", s.OpenBaoConfig, err)
	}
	t.Logf("the vault config this cluster gets, at %s:\n%s", s.OpenBaoConfig, config)
	t.Logf("the command that reads it: %s", strings.Join(argv, " "))

	logPath := filepath.Join(s.DataDir, "vault.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create %s: %v", logPath, err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
		_ = logFile.Close()
	})

	code, body := waitForVault(t, s.DataDir, s.OpenBaoPort, logPath)
	t.Logf("GET %s/v1/sys/health -> HTTP %d %s", VaultAddr(s.OpenBaoPort), code, body)
	if code != 501 {
		t.Fatalf("a fresh vault on a rendered config answered HTTP %d: %s", code, body)
	}
	for _, want := range []string{`"initialized":false`, `"sealed":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the health body does not say %s: %s", want, body)
		}
	}

	gateCtx, gateCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer gateCancel()
	err = waitOpenBao(gateCtx, s.DataDir, s.OpenBaoPort)
	if err == nil {
		t.Fatal("the readiness gate accepted an UNINITIALISED vault: 200/429/473 are the codes a " +
			"serving vault answers, and a vault that answers 501 has no storage backend and no " +
			"identity bundle, so kine would be started against a vault that cannot hold one")
	}
	if !strings.Contains(err.Error(), "HTTP 501") {
		t.Fatalf("the gate refused the vault without naming what it saw: %v", err)
	}
}
