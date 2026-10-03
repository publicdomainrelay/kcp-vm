package clusterprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/identitybundle"
)

func tamperIdentity(t *testing.T, root, tag string) {
	t.Helper()
	for _, rel := range identitybundle.Paths {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(tag+" "+rel+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func TestTheWakeMaterialisesTheIdentityBundleFromTheVaultNotTheFileTree(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	stateDir := t.TempDir()
	const cluster = "liveweb"
	dataDir, err := ClusterDataDir(stateDir, cluster)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	authorClusterIdentity(t, dataDir)
	if err := HarvestBundle(stateDir, cluster); err != nil {
		t.Fatalf("HarvestBundle: %v", err)
	}
	authored := readIdentity(t, dataDir)
	bundleDir, err := BundleDir(stateDir, cluster)
	if err != nil {
		t.Fatalf("BundleDir: %v", err)
	}
	b, err := PersistedBundle(stateDir, cluster)
	if err != nil {
		t.Fatalf("PersistedBundle: %v", err)
	}

	port := freePort(t)
	startClusterVault(t, dataDir, port)
	seal, err := EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("EnsureVault: %v", err)
	}
	if err := ApplyClusterVault(ctx, dataDir, seal); err != nil {
		t.Fatalf("ApplyClusterVault: %v", err)
	}
	if err := WriteBundleToVault(ctx, dataDir, seal, b); err != nil {
		t.Fatalf("WriteBundleToVault: %v", err)
	}

	root := filepath.Join(dataDir, "server")
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("wiping %s: %v", root, err)
	}
	if err := os.RemoveAll(bundleDir); err != nil {
		t.Fatalf("wiping %s: %v", bundleDir, err)
	}
	if err := SeedBundleForWake(ctx, stateDir, cluster); err != nil {
		t.Fatalf("SeedBundleForWake with the whole file tree gone: %v", err)
	}
	if got := readIdentity(t, dataDir); got != authored {
		t.Fatalf("the wake materialised an identity that differs from the vault's:\n%s\n%s", got, authored)
	}
	for _, rel := range identitybundle.Paths {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("the vault materialisation left %s unread: %v", rel, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s is %04o, want 0600: the bundle is a CA key set and k3s reads it as one",
				rel, info.Mode().Perm())
		}
	}

	tamperIdentity(t, root, "stale")
	tamperIdentity(t, bundleDir, "stale")
	if err := SeedBundleForWake(ctx, stateDir, cluster); err != nil {
		t.Fatalf("SeedBundleForWake with a stale file tree over a good vault: %v", err)
	}
	if got := readIdentity(t, dataDir); got != authored {
		t.Fatalf("a stale file tree overrode the vault, so the vault is not the store:\n%s\n%s", got, authored)
	}
}

func TestTheWakeFallsBackToTheFileTreeWhenTheVaultIsEmpty(t *testing.T) {
	skipWithoutBao(t)
	ctx := context.Background()
	stateDir := t.TempDir()
	const cluster = "liveweb"
	dataDir, err := ClusterDataDir(stateDir, cluster)
	if err != nil {
		t.Fatalf("ClusterDataDir: %v", err)
	}
	authorClusterIdentity(t, dataDir)
	if err := HarvestBundle(stateDir, cluster); err != nil {
		t.Fatalf("HarvestBundle: %v", err)
	}
	authored := readIdentity(t, dataDir)
	if err := os.RemoveAll(filepath.Join(dataDir, "server")); err != nil {
		t.Fatalf("wiping the materialisation: %v", err)
	}

	port := freePort(t)
	startClusterVault(t, dataDir, port)
	seal, err := EnsureVault(ctx, dataDir, port)
	if err != nil {
		t.Fatalf("EnsureVault: %v", err)
	}
	if err := ApplyClusterVault(ctx, dataDir, seal); err != nil {
		t.Fatalf("ApplyClusterVault: %v", err)
	}
	if err := SeedBundleFromVault(ctx, stateDir, cluster); !errors.Is(err, ErrNoVaultBundle) {
		t.Fatalf("an initialised vault with no identity bundle answered %v, want ErrNoVaultBundle", err)
	}
	if err := SeedBundleForWake(ctx, stateDir, cluster); err != nil {
		t.Fatalf("SeedBundleForWake with an empty vault and a file-tree bundle: %v", err)
	}
	if got := readIdentity(t, dataDir); got != authored {
		t.Fatalf("the fallback materialised an identity that differs from the file tree's:\n%s\n%s", got, authored)
	}

	if err := os.RemoveAll(filepath.Join(dataDir, "server")); err != nil {
		t.Fatalf("wiping the materialisation: %v", err)
	}
	bundleDir, err := BundleDir(stateDir, cluster)
	if err != nil {
		t.Fatalf("BundleDir: %v", err)
	}
	if err := os.RemoveAll(bundleDir); err != nil {
		t.Fatalf("wiping the file-tree bundle: %v", err)
	}
	if err := SeedBundleForWake(ctx, stateDir, cluster); !errors.Is(err, ErrNoBundleYet) {
		t.Fatalf("an empty vault and no file tree answered %v, want ErrNoBundleYet so k3s "+
			"authors the 13 entries itself on the first bring-up", err)
	}
}
