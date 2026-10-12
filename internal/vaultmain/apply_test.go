package vaultmain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

func TestUnsealFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "root-unseal.json")
	want := unsealFile{
		Threshold:  3,
		Keys:       []string{"a", "b", "c", "d", "e"},
		KeysBase64: []string{"aa", "bb", "cc", "dd", "ee"},
		RootToken:  "root-token",
	}
	if err := writeUnsealFile(path, want); err != nil {
		t.Fatalf("writeUnsealFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("the unseal material is mode %04o, want 0600", got)
	}
	got, err := readUnsealFile(path)
	if err != nil {
		t.Fatalf("readUnsealFile: %v", err)
	}
	if got.RootToken != want.RootToken || got.Threshold != want.Threshold || len(got.Keys) != len(want.Keys) {
		t.Fatalf("round trip returned %+v, want %+v", got, want)
	}
}

func TestReadUnsealFileRejectsShortKeySet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root-unseal.json")
	if err := os.WriteFile(path, []byte(`{"threshold":3,"keys":["a"],"root_token":"t"}`), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	if _, err := readUnsealFile(path); err == nil {
		t.Fatalf("readUnsealFile accepted a threshold of 3 with 1 key")
	}
}

func TestMountMatches(t *testing.T) {
	kv := vaultlayout.Mount{Path: "secret/", Kind: vaultlayout.MountKV}
	if !mountMatches(mountInfo{Type: "kv", Options: map[string]any{"version": "2"}}, kv) {
		t.Fatalf("a kv v2 mount did not match the layout's kv-v2 mount")
	}
	if mountMatches(mountInfo{Type: "kv", Options: map[string]any{"version": "1"}}, kv) {
		t.Fatalf("a kv v1 mount matched the layout's kv-v2 mount")
	}
	pki := vaultlayout.Mount{Path: "pki/", Kind: vaultlayout.MountPKI}
	if !mountMatches(mountInfo{Type: "pki"}, pki) {
		t.Fatalf("a pki mount did not match the layout's pki mount")
	}
	if mountMatches(mountInfo{Type: "kv"}, pki) {
		t.Fatalf("a kv mount matched the layout's pki mount")
	}
}

func TestPlanRootSecretsNamesEveryMissingSource(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "admin.kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig fixture: %v", err)
	}

	planned, err := planRootSecrets(vaultlayout.Default(), Config{
		KCPKubeconfig:     kubeconfig,
		DeviceKeyAudience: "did:web:socialweb.local",
		DevkeyRegistry:    "root:socialweb-provider",
	})
	if err != nil {
		t.Fatalf("planRootSecrets: %v", err)
	}

	byPath := map[string]sourcedSecret{}
	for _, s := range planned {
		byPath[s.Path] = s
	}

	kcp := byPath[vaultlayout.PathKCPAdmin]
	if kcp.Data == nil || kcp.Data["kubeconfig"] != "apiVersion: v1\n" {
		t.Fatalf("the kcp admin secret is %+v, want the kubeconfig file's bytes", kcp)
	}
	device := byPath[vaultlayout.PathDeviceKey]
	if device.Data == nil || device.Data["audience"] != "did:web:socialweb.local" {
		t.Fatalf("the device-key secret is %+v, want the audience from the environment", device)
	}
	for _, path := range []string{
		vaultlayout.PathKCPAdminTokenStore,
		vaultlayout.PathKCPClientCA,
		vaultlayout.PathContainerTLS,
	} {
		if byPath[path].Reason == "" {
			t.Fatalf("the secret at %s has no data and no named reason, so a reader cannot tell an "+
				"absent source from a bug", path)
		}
	}
}

func TestPlanRootSecretsFailsOnMissingKubeconfig(t *testing.T) {
	if _, err := planRootSecrets(vaultlayout.Default(), Config{
		KCPKubeconfig: filepath.Join(t.TempDir(), "absent.kubeconfig"),
	}); err == nil {
		t.Fatalf("planRootSecrets accepted a missing kcp admin kubeconfig, which is the one root secret the gateway cannot start without")
	}
}

func TestLoadRejectsRelativeStateDir(t *testing.T) {
	get := func(name string) string {
		if name == envStateDir {
			return "relative/state"
		}
		return ""
	}
	if _, err := load(get); err == nil {
		t.Fatalf("load accepted a relative state dir")
	}
}

func TestLoadDerivesUnsealPathAndScheme(t *testing.T) {
	get := func(name string) string {
		switch name {
		case envStateDir:
			return "/var/lib/socialweb"
		case envAddr:
			return "127.0.0.1:8200"
		default:
			return ""
		}
	}
	cfg, err := load(get)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.UnsealPath != "/var/lib/socialweb/openbao/root-unseal.json" {
		t.Fatalf("unseal path is %q, want /var/lib/socialweb/openbao/root-unseal.json", cfg.UnsealPath)
	}
	if cfg.Addr != "https://127.0.0.1:8200" {
		t.Fatalf("addr is %q, want https://127.0.0.1:8200", cfg.Addr)
	}
}
