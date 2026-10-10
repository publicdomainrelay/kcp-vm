//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
)

const twoContextKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: root
  cluster:
    server: https://127.0.0.1:17443/clusters/root
    insecure-skip-tls-verify: true
- name: system:admin
  cluster:
    server: https://127.0.0.1:17443/clusters/system:admin
    insecure-skip-tls-verify: true
contexts:
- name: root
  context:
    cluster: root
    user: kcp-admin
- name: system:admin
  context:
    cluster: system:admin
    user: shard-admin
current-context: root
users:
- name: kcp-admin
  user:
    token: the-shard-tenants-own-token
- name: shard-admin
  user:
    token: the-shard-admins-token
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.kubeconfig")
	if err := os.WriteFile(path, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	return path
}

func TestTheMountChildWriterIsBuiltFromTheNamedContext(t *testing.T) {
	path := writeKubeconfig(t)
	cfg := Config{
		KCPHost:       "https://127.0.0.1:6443",
		KCPKubeconfig: path,
		MountContext:  kcpclient.ShardAdminContext,
	}

	if _, err := mountChildWriter(cfg); err != nil {
		t.Fatalf("mountChildWriter with kcp's own %s context: %v", kcpclient.ShardAdminContext, err)
	}

	cfg.MountContext = "shard-admin"
	_, err := mountChildWriter(cfg)
	if err == nil {
		t.Fatal("a context this kubeconfig does not hold produced a mount-child client: " +
			"whatever identity it fell back to is the one writing spec.URL")
	}
	for _, want := range []string{envMountContext, "shard-admin", path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestTheMountContextDefaultsToKcpsShardAdmin(t *testing.T) {
	cfg, err := load(envFor(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MountContext != kcpclient.ShardAdminContext {
		t.Fatalf("the mount context defaults to %q, want %q: kcp writes that context into "+
			"every admin kubeconfig it generates, and the identity that runs the tree cannot "+
			"write spec.URL", cfg.MountContext, kcpclient.ShardAdminContext)
	}

	cfg, err = load(envFor(map[string]string{envMountContext: "root"}))
	if err != nil {
		t.Fatalf("load with %s set: %v", envMountContext, err)
	}
	if cfg.MountContext != "root" {
		t.Fatalf("the mount context is %q, want the configured root", cfg.MountContext)
	}
}
