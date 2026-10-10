package openbao

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not lend a port to the test vault: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

const baoStartAttempts = 3

func startBao(t *testing.T) (*Client, string) {
	t.Helper()
	if _, err := exec.LookPath("bao"); err != nil {
		livegate.RequireLocal(t, "bao is not on PATH, so this test skipped: a skip is not a pass and "+
			"leaves the OpenBao client unproven. Install OpenBao v2.6.2 to prove it.")
		return nil, ""
	}
	for attempt := 1; ; attempt++ {
		client, addr, err := startBaoOnce(t)
		if err == nil {
			return client, addr
		}
		if attempt >= baoStartAttempts {
			t.Fatalf("%s. Tried %d times with a fresh port each time, which is what this retry is "+
				"for: `freePort` listens on :0, reads the number and CLOSES the listener before the "+
				"vault binds it, so between the close and the bind the number is free for anyone -- "+
				"including a second `go test` of this repo running at the same time. The last "+
				"attempt's address was %s", err, baoStartAttempts, addr)
		}
	}
}

func startBaoOnce(t *testing.T) (*Client, string, error) {
	t.Helper()
	dir := t.TempDir()
	port := freePort(t)
	storage := filepath.Join(dir, "data")
	cfg := fmt.Sprintf(`ui = false
disable_clustering = true
api_addr = "http://127.0.0.1:%[1]d"
listener "tcp" {
  address     = "127.0.0.1:%[1]d"
  tls_disable = true
}
storage "file" {
  path = "%[2]s"
}
`, port, storage)
	cfgPath := filepath.Join(dir, "openbao.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("could not write the vault config %q: %v", cfgPath, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "bao", "server", "-config="+cfgPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("could not start bao server with config %q: %v", cfgPath, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	})
	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := New(addr, "")
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-exited:
			cancel()
			return nil, addr, fmt.Errorf("the vault exited before it answered on %s, and its own "+
				"stderr is the reason -- the likeliest is that something else already holds the "+
				"address, because `freePort` gives it back before the vault binds it: %v\n%s",
				addr, err, stderr.String())
		default:
		}
		_, err := client.Health(context.Background())
		if err == nil {
			return client, addr, nil
		}
		if !errors.Is(err, ErrTransport) {
			cancel()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
			}
			return nil, addr, fmt.Errorf("the vault answered before it was ready, which this gate "+
				"did not expect, and the likeliest reason is that the address answered by something "+
				"else entirely: %w", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("bao never answered on %s within 30s. Its stderr follows:\n%s", addr, stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func parsedCert(t *testing.T, label, pemText string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		t.Fatalf("%s is not PEM, so it cannot chain to anything: %q", label, pemText)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("%s is PEM but not a certificate: %v", label, err)
	}
	return cert
}

func TestClientAgainstRealBao(t *testing.T) {
	root, addr := startBao(t)
	ctx := context.Background()

	health, err := root.Health(ctx)
	if err != nil {
		t.Fatalf("Health on a fresh vault: %v", err)
	}
	if health.Initialized {
		t.Fatalf("a fresh vault reports initialized, so this test would prove nothing about init: %+v", health)
	}
	if !health.Sealed {
		t.Fatalf("a fresh vault reports unsealed, so this test would prove nothing about unseal: %+v", health)
	}
	initialized, err := root.Initialized(ctx)
	if err != nil {
		t.Fatalf("Initialized before init: %v", err)
	}
	if initialized {
		t.Fatalf("Initialized returned true on a fresh vault")
	}
	sealed, err := root.Sealed(ctx)
	if err != nil {
		t.Fatalf("Sealed before init: %v", err)
	}
	if !sealed {
		t.Fatalf("Sealed returned false on a fresh vault")
	}

	result, err := root.Init(ctx, 5, 3)
	if err != nil {
		t.Fatalf("Init(5, 3): %v", err)
	}
	if result.RootToken == "" {
		t.Fatalf("Init returned no root token: %+v", result)
	}
	if len(result.Keys) != 5 || len(result.KeysBase64) != 5 {
		t.Fatalf("Init(5, 3) returned %d keys and %d base64 keys, want 5 each: %+v", len(result.Keys), len(result.KeysBase64), result)
	}

	root = New(addr, result.RootToken)

	sealed, err = root.Unseal(ctx, result.Keys[0])
	if err != nil {
		t.Fatalf("Unseal with key 1 of 3: %v", err)
	}
	if !sealed {
		t.Fatalf("the vault unsealed after 1 of a 3-of-5 threshold, so the threshold is not enforced")
	}
	if _, err = root.Unseal(ctx, result.Keys[1]); err != nil {
		t.Fatalf("Unseal with key 2 of 3: %v", err)
	}
	sealed, err = root.Unseal(ctx, result.Keys[2])
	if err != nil {
		t.Fatalf("Unseal with key 3 of 3: %v", err)
	}
	if sealed {
		t.Fatalf("the vault is still sealed after the 3rd of a 3-of-5 threshold")
	}
	health, err = root.Health(ctx)
	if err != nil {
		t.Fatalf("Health after unseal: %v", err)
	}
	if !health.Initialized || health.Sealed {
		t.Fatalf("after init and unseal the vault reports initialized=%t sealed=%t", health.Initialized, health.Sealed)
	}

	if err := root.EnableKV(ctx, "kv", 2); err != nil {
		t.Fatalf("EnableKV(kv, 2): %v", err)
	}
	secret := map[string]any{"username": "node", "password": "s3cret"}
	if err := root.PutKV(ctx, "kv/data/app/config", secret); err != nil {
		t.Fatalf("PutKV: %v", err)
	}
	got, err := root.GetKV(ctx, "kv/data/app/config")
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	if got["username"] != "node" || got["password"] != "s3cret" {
		t.Fatalf("GetKV returned %v, want the map that PutKV stored", got)
	}
	if _, err := root.GetKV(ctx, "kv/data/app/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetKV on a missing secret returned %v, want ErrNotFound so a caller can tell unconfigured from denied", err)
	}

	policy := `path "kv/data/app/*" {
  capabilities = ["read"]
}
`
	if err := root.PutPolicy(ctx, "app-read", policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	returned, err := root.GetPolicy(ctx, "app-read")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if !strings.Contains(returned, "kv/data/app/*") || !strings.Contains(returned, "read") {
		t.Fatalf("GetPolicy returned %q, which does not carry the HCL that PutPolicy stored", returned)
	}

	if err := root.MountPKI(ctx, "pki"); err != nil {
		t.Fatalf("MountPKI: %v", err)
	}
	if err := root.ConfigurePKIURLs(ctx, "pki", "http://127.0.0.1/issuing", "http://127.0.0.1/crl"); err != nil {
		t.Fatalf("ConfigurePKIURLs: %v", err)
	}
	rootCert, err := root.GeneratePKIRoot(ctx, "pki", "socialweb.test", "87600h")
	if err != nil {
		t.Fatalf("GeneratePKIRoot: %v", err)
	}
	if rootCert.Certificate == "" {
		t.Fatalf("GeneratePKIRoot returned no certificate: %+v", rootCert)
	}
	if err := root.WritePKIRole(ctx, "pki", "server", PKIRole{
		AllowedDomains:  []string{"socialweb.test"},
		AllowSubdomains: true,
		MaxTTL:          "8760h",
	}); err != nil {
		t.Fatalf("WritePKIRole: %v", err)
	}
	issued, err := root.IssueCert(ctx, "pki", "server", CertRequest{
		CommonName: "node1.socialweb.test",
		TTL:        "24h",
	})
	if err != nil {
		t.Fatalf("IssueCert: %v", err)
	}
	if issued.Certificate == "" || issued.PrivateKey == "" || issued.Serial == "" {
		t.Fatalf("IssueCert returned an incomplete certificate: %+v", issued)
	}
	leaf := parsedCert(t, "the issued leaf certificate", issued.Certificate)
	authority := parsedCert(t, "the generated root certificate", rootCert.Certificate)
	pool := x509.NewCertPool()
	pool.AddCert(authority)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "node1.socialweb.test"}); err != nil {
		t.Fatalf("the issued leaf does not chain to the generated root: %v", err)
	}

	if err := root.EnableAppRole(ctx); err != nil {
		t.Fatalf("EnableAppRole: %v", err)
	}
	if err := root.WriteAppRole(ctx, "node", []string{"app-read"}, "1h"); err != nil {
		t.Fatalf("WriteAppRole: %v", err)
	}
	roleID, err := root.AppRoleRoleID(ctx, "node")
	if err != nil {
		t.Fatalf("AppRoleRoleID: %v", err)
	}
	secretID, err := root.AppRoleSecretID(ctx, "node")
	if err != nil {
		t.Fatalf("AppRoleSecretID: %v", err)
	}
	if roleID == "" || secretID == "" {
		t.Fatalf("AppRoleRoleID=%q and AppRoleSecretID=%q, both must be non-empty to log in", roleID, secretID)
	}
	token, err := root.LoginAppRole(ctx, roleID, secretID)
	if err != nil {
		t.Fatalf("LoginAppRole: %v", err)
	}
	if token == "" {
		t.Fatalf("LoginAppRole returned an empty token")
	}
	scoped := New(addr, token)
	allowed, err := scoped.GetKV(ctx, "kv/data/app/config")
	if err != nil {
		t.Fatalf("the AppRole token could not read the path its policy allows: %v", err)
	}
	if allowed["username"] != "node" {
		t.Fatalf("the AppRole token read %v, want the stored secret", allowed)
	}
	if _, err := scoped.GetKV(ctx, "kv/data/other/secret"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the AppRole token read a path outside its policy: got %v, want ErrForbidden", err)
	}

	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not borrow a dead port: %v", err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()
	if _, err := New("http://"+deadAddr, "").Health(ctx); !errors.Is(err, ErrTransport) {
		t.Fatalf("Health against a dead port returned %v, want ErrTransport so a caller can tell a down vault from a denied one", err)
	}
}
