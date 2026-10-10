package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const k3sAssetName = "5a9973ddf4c7ec074f657c06287e0e6a07a24ecafd6d326827f70ef1e95bdd2d"

func sha256Of(t *testing.T, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func k3sAssetFixture(t *testing.T) string {
	t.Helper()
	asset := filepath.Join(t.TempDir(), "data", k3sAssetName)
	bin := filepath.Join(asset, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("creating %s: %v", bin, err)
	}
	mu := "x"
	files := map[string]string{"k3s": mu, "busybox": "b", "cni": "c"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	links := map[string]string{"addgroup": "busybox", "bridge": "cni", "loopback": "cni", "kubectl": "k3s"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(bin, name)); err != nil {
			t.Fatalf("linking %s: %v", name, err)
		}
	}
	var sums strings.Builder
	for name, body := range files {
		fmt.Fprintf(&sums, "%s  %s\n", sha256Of(t, body), name)
	}
	if err := os.WriteFile(filepath.Join(bin, ".sha256sums"), []byte(sums.String()), 0o644); err != nil {
		t.Fatalf("writing .sha256sums: %v", err)
	}
	var listed strings.Builder
	for name, target := range links {
		fmt.Fprintf(&listed, "%s %s\n", name, target)
	}
	if err := os.WriteFile(filepath.Join(bin, ".links"), []byte(listed.String()), 0o644); err != nil {
		t.Fatalf("writing .links: %v", err)
	}
	return asset
}

func TestK3sAssetDirIsReadFromTheDebugLineItPrints(t *testing.T) {
	line := `time="2026-10-07T16:57:58-07:00" level=debug msg="Asset dir /var/lib/rancher/k3s/data/` +
		k3sAssetName + `"` + "\n"
	dir, err := parseK3sAssetDir(line)
	if err != nil {
		t.Fatalf("a debug line naming the asset directory was refused: %v", err)
	}
	if want := "/var/lib/rancher/k3s/data/" + k3sAssetName; dir != want {
		t.Fatalf("read %q, want %q", dir, want)
	}
}

func TestARunThatNamesNoAssetDirectoryIsRefused(t *testing.T) {
	if _, err := parseK3sAssetDir("level=info msg=\"Preparing data dir\"\n"); err == nil {
		t.Fatal("a run with no asset directory was accepted, and the build would guess at one")
	}
}

func TestTwoAssetDirectoriesInOneRunAreRefused(t *testing.T) {
	output := "level=debug msg=\"Asset dir /a/data/one\"\nlevel=debug msg=\"Asset dir /a/data/two\"\n"
	if _, err := parseK3sAssetDir(output); err == nil {
		t.Fatal("two asset directories were accepted and one of them was picked by line order")
	}
}

func TestTheStagedAssetsAreCheckedAgainstTheirOwnSumsBeforeTheImageIsWritten(t *testing.T) {
	cfg := validConfig(t)
	asset := k3sAssetFixture(t)
	if err := stageK3sAssets(cfg, asset); err != nil {
		t.Fatalf("staging an intact asset directory: %v", err)
	}
	data := filepath.Join(cfg.StageDir, cfg.K3sDataDir, "data")
	if _, err := os.Stat(filepath.Join(data, k3sAssetName, "bin", "k3s")); err != nil {
		t.Fatalf("the multicall binary is not in the image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, k3sAssetName, "bin", "addgroup")); err != nil {
		t.Fatalf("a link from the asset's bin directory is not in the image: %v", err)
	}
	current, err := os.Readlink(filepath.Join(data, "current"))
	if err != nil {
		t.Fatalf("data/current is not a link, so the guest's k3s has no fast path: %v", err)
	}
	want := filepath.Join(cfg.K3sDataDir, "data", k3sAssetName)
	if current != want {
		t.Fatalf("data/current points at %q, want the guest's own %q: a link naming this host is a "+
			"directory that does not exist inside the guest", current, want)
	}
	cni, err := os.Readlink(filepath.Join(data, "cni", "cni"))
	if err != nil {
		t.Fatalf("data/cni/cni is not a link: %v", err)
	}
	if cni != filepath.Join(want, "bin", "cni") {
		t.Fatalf("data/cni/cni points at %q, want %q", cni, filepath.Join(want, "bin", "cni"))
	}
	for _, plugin := range []string{"bridge", "loopback"} {
		got, err := os.Readlink(filepath.Join(data, "cni", plugin))
		if err != nil {
			t.Fatalf("data/cni/%s is not a link, so containerd finds no plugin under its own bin dir: %v", plugin, err)
		}
		if got != cni {
			t.Fatalf("data/cni/%s points at %q, want %q", plugin, got, cni)
		}
	}
}

func TestAnAssetDirectoryWhoseSumsDoNotMatchIsRefused(t *testing.T) {
	cfg := validConfig(t)
	asset := k3sAssetFixture(t)
	if err := os.WriteFile(filepath.Join(asset, "bin", "k3s"), []byte("y"), 0o755); err != nil {
		t.Fatalf("tampering with the multicall binary: %v", err)
	}
	err := stageK3sAssets(cfg, asset)
	if err == nil {
		t.Fatal("an asset directory that k3s itself would refuse was staged into an image")
	}
	if !strings.Contains(err.Error(), ".sha256sums") {
		t.Fatalf("the refusal does not name the sums it failed: %v", err)
	}
}

func TestAnAssetDirectoryWithNoSumsAtAllIsRefused(t *testing.T) {
	cfg := validConfig(t)
	asset := k3sAssetFixture(t)
	if err := os.Remove(filepath.Join(asset, "bin", ".sha256sums")); err != nil {
		t.Fatalf("removing .sha256sums: %v", err)
	}
	if err := stageK3sAssets(cfg, asset); err == nil {
		t.Fatal("a directory with nothing to check it against was staged, so nothing was checked")
	}
}

func TestAnAssetDirectoryWithNoLinksAtAllIsRefused(t *testing.T) {
	cfg := validConfig(t)
	asset := k3sAssetFixture(t)
	if err := os.Remove(filepath.Join(asset, "bin", ".links")); err != nil {
		t.Fatalf("removing .links: %v", err)
	}
	if err := stageK3sAssets(cfg, asset); err == nil {
		t.Fatal("a directory whose links were never checked was staged")
	}
}

func TestAnAssetDirectoryAlreadyInsideTheStageIsNotCopiedOverItself(t *testing.T) {
	cfg := validConfig(t)
	inside := filepath.Join(cfg.StageDir, cfg.K3sDataDir, "data", k3sAssetName)
	if err := os.MkdirAll(filepath.Join(inside, "bin"), 0o755); err != nil {
		t.Fatalf("creating %s: %v", inside, err)
	}
	body := "x"
	if err := os.WriteFile(filepath.Join(inside, "bin", "k3s"), []byte(body), 0o755); err != nil {
		t.Fatalf("writing the multicall binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inside, "bin", ".sha256sums"), []byte(sha256Of(t, body)+"  k3s\n"), 0o644); err != nil {
		t.Fatalf("writing .sha256sums: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inside, "bin", ".links"), []byte("kubectl k3s\n"), 0o644); err != nil {
		t.Fatalf("writing .links: %v", err)
	}
	if err := os.Symlink("k3s", filepath.Join(inside, "bin", "kubectl")); err != nil {
		t.Fatalf("linking kubectl: %v", err)
	}
	if err := stageK3sAssets(cfg, inside); err != nil {
		t.Fatalf("staging an asset directory k3s unpacked straight into the stage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inside, "bin", "k3s")); err != nil {
		t.Fatalf("the multicall binary is gone after staging: %v", err)
	}
}
