package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheImageBuilderCopiesTheInitAssetAndNeverRunsGo(t *testing.T) {
	t.Setenv("PATH", "")
	root := t.TempDir()
	asset := filepath.Join(root, "assets", "socialweb-nodeinit")
	if err := os.MkdirAll(filepath.Dir(asset), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("the guest init, built elsewhere\n")
	if err := os.WriteFile(asset, body, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "stage", "kcpinit")
	if err := buildInit(Config{InitAsset: asset, InitPackage: "./cmd/socialweb-nodeinit", InitBinary: dst}); err != nil {
		t.Fatalf("buildInit with an init asset and no toolchain on PATH: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("the staged init is %q (%v), want the asset's bytes", got, err)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the staged init is not executable: %v %v", fi, err)
	}
	if err := buildInit(Config{InitAsset: dst, InitBinary: dst}); err != nil {
		t.Fatalf("an asset that is already the staged init: %v", err)
	}

	err = buildInit(Config{InitPackage: "./cmd/socialweb-nodeinit", InitBinary: filepath.Join(root, "other")})
	if err == nil || !strings.Contains(err.Error(), "init_asset") || !strings.Contains(err.Error(), "-init-asset") {
		t.Fatalf("no init asset: %v; the builder never builds the init and must name the option that supplies it", err)
	}
}
