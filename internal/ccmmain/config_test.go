package ccmmain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
)

func getter(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	kube := writeFile(t, dir, "tenant.kubeconfig", "apiVersion: v1\nkind: Config\n")
	cfg, err := load(getter(map[string]string{
		envTenantKubeconfig: kube,
		envCluster:          "c1",
		envInstancesFile:    filepath.Join(dir, "nodes.json"),
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MaxNodes != DefaultMaxNodes {
		t.Fatalf("MaxNodes = %d, want %d", cfg.MaxNodes, DefaultMaxNodes)
	}
	if cfg.PodsPerNode != DefaultPodsPerNode {
		t.Fatalf("PodsPerNode = %d, want %d", cfg.PodsPerNode, DefaultPodsPerNode)
	}
	if cfg.Resync != 30*time.Second {
		t.Fatalf("Resync = %s, want 30s", cfg.Resync)
	}
}

func TestLoadRequiresTheThreeInputs(t *testing.T) {
	dir := t.TempDir()
	kube := writeFile(t, dir, "tenant.kubeconfig", "apiVersion: v1\nkind: Config\n")
	full := map[string]string{
		envTenantKubeconfig: kube,
		envCluster:          "c1",
		envInstancesFile:    filepath.Join(dir, "nodes.json"),
	}
	for _, drop := range []string{envTenantKubeconfig, envCluster, envInstancesFile} {
		t.Run(drop, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range full {
				if k != drop {
					env[k] = v
				}
			}
			if _, err := load(getter(env)); err == nil {
				t.Fatalf("load without %s succeeded, want a refusal", drop)
			}
		})
	}
}

func TestLoadRefusesTheK3sAdminKubeconfigByName(t *testing.T) {
	dir := t.TempDir()
	admin := writeFile(t, dir, clusterstack.AdminKubeconfigName, "apiVersion: v1\nkind: Config\n")
	_, err := load(getter(map[string]string{
		envTenantKubeconfig: admin,
		envCluster:          "c1",
		envInstancesFile:    filepath.Join(dir, "nodes.json"),
	}))
	if err == nil {
		t.Fatal("load accepted k3s's admin kubeconfig by name, want a refusal")
	}
	if !strings.Contains(err.Error(), "MINTED") || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("the refusal does not name the minted credential it wants: %v", err)
	}
}

func TestLoadRefusesAStaticBearerToken(t *testing.T) {
	dir := t.TempDir()
	body := "apiVersion: v1\nkind: Config\nusers:\n- name: x\n  user:\n    token: abc\n"
	bad := writeFile(t, dir, "tenant.kubeconfig", body)
	_, err := load(getter(map[string]string{
		envTenantKubeconfig: bad,
		envCluster:          "c1",
		envInstancesFile:    filepath.Join(dir, "nodes.json"),
	}))
	if err == nil {
		t.Fatal("load accepted a static bearer token, want a refusal")
	}
}

func TestLoadRefusesAMissingTenantKubeconfig(t *testing.T) {
	dir := t.TempDir()
	_, err := load(getter(map[string]string{
		envTenantKubeconfig: filepath.Join(dir, "absent.kubeconfig"),
		envCluster:          "c1",
		envInstancesFile:    filepath.Join(dir, "nodes.json"),
	}))
	if err == nil {
		t.Fatal("load accepted a path that does not exist, want a refusal")
	}
}

func TestLoadOverridesTheOptionalInputs(t *testing.T) {
	dir := t.TempDir()
	kube := writeFile(t, dir, "tenant.kubeconfig", "apiVersion: v1\nkind: Config\n")
	cfg, err := load(getter(map[string]string{
		envTenantKubeconfig: kube,
		envCluster:          "c1",
		envInstancesFile:    filepath.Join(dir, "nodes.json"),
		envMaxNodes:         "9",
		envPodsPerNode:      "16",
		envResync:           "5s",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MaxNodes != 9 || cfg.PodsPerNode != 16 || cfg.Resync != 5*time.Second {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRefusesNonPositiveNumbersAndDurations(t *testing.T) {
	dir := t.TempDir()
	kube := writeFile(t, dir, "tenant.kubeconfig", "apiVersion: v1\nkind: Config\n")
	base := map[string]string{
		envTenantKubeconfig: kube,
		envCluster:          "c1",
		envInstancesFile:    filepath.Join(dir, "nodes.json"),
	}
	for _, extra := range []map[string]string{
		{envMaxNodes: "0"},
		{envMaxNodes: "many"},
		{envPodsPerNode: "0"},
		{envResync: "0s"},
		{envResync: "soon"},
	} {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		if _, err := load(getter(env)); err == nil {
			t.Fatalf("load accepted %v, want a refusal", extra)
		}
	}
}
