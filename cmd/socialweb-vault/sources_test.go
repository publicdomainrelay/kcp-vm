package main

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const testCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func kcpRootFixture(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	kubeconfig := filepath.Join(root, "admin.kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig fixture: %v", err)
	}
	t.Setenv(envKCPRootDir, root)
	return Config{KCPKubeconfig: kubeconfig}
}

func planByPath(t *testing.T, cfg Config) map[string]sourcedSecret {
	t.Helper()
	planned, err := planRootSecrets(vaultlayout.Default(), cfg)
	if err != nil {
		t.Fatalf("planRootSecrets: %v", err)
	}
	byPath := map[string]sourcedSecret{}
	for _, s := range planned {
		byPath[s.Path] = s
	}
	return byPath
}

func TestKCPAdminTokenStoreProducerWritesTheHash(t *testing.T) {
	cfg := kcpRootFixture(t)
	raw := []byte{0x00, 0x01, 0x02, 0xfe, 0xff}
	if err := os.WriteFile(kcpAdminTokenStorePath(cfg), raw, 0o600); err != nil {
		t.Fatalf("writing the token store fixture: %v", err)
	}
	s := planByPath(t, cfg)[vaultlayout.PathKCPAdminTokenStore]
	if s.Data == nil {
		t.Fatalf("the kcp admin token store was not produced: %+v", s)
	}
	if got := s.Data["token-hash"]; got != hex.EncodeToString(raw) {
		t.Fatalf("token-hash is %v, want %s", got, hex.EncodeToString(raw))
	}
}

func TestMissingKCPAdminTokenStoreIsRequired(t *testing.T) {
	cfg := kcpRootFixture(t)
	s := planByPath(t, cfg)[vaultlayout.PathKCPAdminTokenStore]
	if s.Data != nil || !s.Required || s.Reason == "" {
		t.Fatalf("a kcp root with no .admin-token-store planned %+v, want a required source "+
			"with a named reason", s)
	}
}

func TestMissingKCPAdminTokenStoreFailsTheRun(t *testing.T) {
	cfg := kcpRootFixture(t)
	layout := vaultlayout.Layout{
		Secrets: []vaultlayout.Secret{{
			Vault: vaultlayout.Root,
			Path:  vaultlayout.PathKCPAdminTokenStore,
			Class: vaultlayout.ClassKCPAdminTokenStore,
		}},
	}
	err := applySecrets(context.Background(), cfg, nil, layout, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err == nil {
		t.Fatalf("applySecrets returned nil for a required kcp source that does not exist, so the " +
			"run warned where it must fail")
	}
}

func TestKCPClientCAProducerReadsTheRootVaultPKI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/pki/ca/pem" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(testCA))
	}))
	defer server.Close()

	pem, err := fetchPKICAPEM(context.Background(), newAPI(server.URL, "token", nil), vaultlayout.MountPKIRoot)
	if err != nil {
		t.Fatalf("fetchPKICAPEM: %v", err)
	}
	if pem != strings.TrimSpace(testCA) {
		t.Fatalf("the client CA is %q, want the PEM the PKI mount served", pem)
	}
}

func TestKCPClientCAEmptyCAFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("\n"))
	}))
	defer server.Close()

	if _, err := fetchPKICAPEM(context.Background(), newAPI(server.URL, "token", nil), vaultlayout.MountPKIRoot); err == nil {
		t.Fatalf("an empty PKI CA was accepted, so kcp would be handed a --client-ca-file that " +
			"trusts no client")
	}
}

func TestUnreachableRootVaultMakesTheKCPClientCARequired(t *testing.T) {
	cfg := kcpRootFixture(t)
	s := planByPath(t, cfg)[vaultlayout.PathKCPClientCA]
	if s.Data != nil || !s.Required || s.Reason == "" {
		t.Fatalf("a client CA that cannot be read planned %+v, want a required source with a "+
			"named reason", s)
	}
}
