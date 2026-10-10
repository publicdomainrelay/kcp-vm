package kcpclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestAConfigFromAContextIsThatContextsIdentity(t *testing.T) {
	path := writeKubeconfig(t)

	cfg, err := ConfigFromContext(path, ShardAdminContext)
	if err != nil {
		t.Fatalf("ConfigFromContext(%q): %v", ShardAdminContext, err)
	}
	if cfg.BearerToken != "the-shard-admins-token" {
		t.Fatalf("the config carries the token %q, want the shard admin's. The mount child "+
			"carries spec.URL, which kcp accepts only from a system privileged user, so a client "+
			"built from the wrong context is refused with `spec.URL can only be set by system "+
			"privileged users` and the mount serves kcp itself", cfg.BearerToken)
	}

	unprivileged, err := ConfigFromContext(path, "root")
	if err != nil {
		t.Fatalf("ConfigFromContext(root): %v", err)
	}
	if unprivileged.BearerToken != "the-shard-tenants-own-token" {
		t.Fatalf("the root context's token = %q, want the kcp-admin one", unprivileged.BearerToken)
	}
	if unprivileged.BearerToken == cfg.BearerToken {
		t.Fatal("the two contexts produced one identity: the split between the identity that " +
			"runs the tree and the one that writes spec.URL is the whole point of naming a " +
			"second context")
	}
}

func TestAContextThatIsNotThereIsRefused(t *testing.T) {
	path := writeKubeconfig(t)

	_, err := ConfigFromContext(path, "shard-admin")
	if err == nil {
		t.Fatal("a context name no kubeconfig holds produced a config: whatever identity it " +
			"fell back to would be the one writing spec.URL")
	}
	if !strings.Contains(err.Error(), "shard-admin") {
		t.Fatalf("the refusal does not name the context it looked for: %v", err)
	}

	if _, err := ConfigFromContext("", ShardAdminContext); err == nil {
		t.Fatal("an empty kubeconfig path produced a config")
	}
	if _, err := ConfigFromContext(path, ""); err == nil {
		t.Fatal("an empty context name produced a config")
	}
}
