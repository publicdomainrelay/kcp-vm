//go:build linux

package main

import (
	"context"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

func requirePostgresBinaries(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts a real postgres and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	for _, bin := range []string{"initdb", "postgres", "psql"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH: the vault's store is this cluster's own embedded-postgres, so there is no vault store to start", bin)
		}
	}
}

func vaultStoreAllocator(t *testing.T) *portallocator.Allocator {
	t.Helper()
	rng, err := liveports.TakeForProcess(liveports.PostgresWindows)
	if err != nil {
		t.Fatalf("liveports.TakeForProcess: %v. The product runs on the default carve (20000-29999), "+
			"so a test that starts a real postgres must lease outside it", err)
	}
	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "ports.json"),
		Range:     rng,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	return alloc
}

func TestAnExternalKineStoreStillStartsThePostgresTheVaultNeeds(t *testing.T) {
	requirePostgresBinaries(t)
	ctx := context.Background()
	cfg := Config{StateDir: t.TempDir(), StoreEndpoint: "postgres://operator@external:5432/k3s"}
	alloc := vaultStoreAllocator(t)
	reg := newPostgresRegistry()
	t.Cleanup(func() { _ = reg.stopAll(ctx) })

	endpoint, err := openStore(ctx, cfg, alloc, reg, "liveweb", testLogger(), true)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if endpoint != cfg.StoreEndpoint {
		t.Fatalf("kine's endpoint is %q, want the operator's external store %q", endpoint, cfg.StoreEndpoint)
	}
	pg, ok := reg.get("liveweb")
	if !ok {
		t.Fatal("no per-cluster postgres was started for the vault, so the vault step would start " +
			"on no store and the deployment would silently run without custody")
	}
	if !pg.Up() {
		t.Fatal("the postgres the vault needs is not up")
	}
	vdsn, err := reg.vaultDSN("liveweb")
	if err != nil {
		t.Fatalf("vaultDSN: %v", err)
	}
	u, err := url.Parse(vdsn)
	if err != nil {
		t.Fatalf("parsing the vault DSN: %v", err)
	}
	if got := strings.TrimPrefix(u.Path, "/"); got != clusterstack.OpenBaoDatabase {
		t.Fatalf("the vault DSN names database %q, want %q", got, clusterstack.OpenBaoDatabase)
	}
	if got, err := storeEndpoint(cfg, reg)("liveweb"); err != nil || got != cfg.StoreEndpoint {
		t.Fatalf("kine's endpoint closure returned %q (err %v), want the external store %q",
			got, err, cfg.StoreEndpoint)
	}
}

func TestAnExternalKineStoreWithNoVaultStartsNoPostgres(t *testing.T) {
	cfg := Config{StateDir: t.TempDir(), StoreEndpoint: "postgres://operator@external:5432/k3s"}
	alloc := testAllocator(t)
	reg := newPostgresRegistry()

	endpoint, err := openStore(context.Background(), cfg, alloc, reg, "liveweb", testLogger(), false)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if endpoint != cfg.StoreEndpoint {
		t.Fatalf("kine's endpoint is %q, want the external store %q", endpoint, cfg.StoreEndpoint)
	}
	if _, ok := reg.get("liveweb"); ok {
		t.Fatal("a stack with no vault and an external store started a per-cluster postgres")
	}
}
