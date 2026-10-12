package kcpstore

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTheBidderKcpStoreIsOneKineOnSqliteAndNoPostgres(t *testing.T) {
	spec, err := KineSqliteSpec(KineConfig{Unit: "socialweb-kcp-abcd1234-kine-a.service", Slice: "socialweb-kcp-abcd1234.slice",
		Exec: "/opt/kine", Dir: "/s/kcp/kine-a"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(spec.Exec, " ")
	if !strings.Contains(argv, "--endpoint sqlite:///s/kcp/kine-a/kine.db") || strings.Contains(argv, "postgres") {
		t.Fatalf("argv %s", argv)
	}
	if !slices.Contains(spec.Exec, "127.0.0.1:0") || !slices.Contains(spec.Exec, "--metrics-bind-address=0") {
		t.Fatalf("kine must bind port 0 and serve no metrics port: %s", argv)
	}
}

func TestKineRefusesARelativeExecutable(t *testing.T) {
	if _, err := KineSqliteArgv(KineConfig{Exec: "kine", Dir: "/s"}); err == nil {
		t.Fatal("a PATH lookup name was accepted")
	}
}

func TestTheDevQuickProfileUsesSqliteAndTheBedProfilePostgres(t *testing.T) {
	cases := []struct {
		role    Role
		profile string
		want    Kind
	}{
		{RoleGateway, "minimal", KindKineSqlite},
		{RoleGateway, "quick", KindKineSqlite},
		{RoleGateway, "bed", KindKinePostgres},
		{RoleGateway, "prodlike", KindKinePostgres},
		{RoleGateway, "", KindKinePostgres},
		{RoleBidder, "minimal", KindKineSqlite},
		{RoleBidder, "bed", KindKineSqlite},
		{RoleBidder, "", KindKineSqlite},
	}
	for _, c := range cases {
		got, err := StoreFor(c.role, c.profile)
		if err != nil || got != c.want {
			t.Errorf("%s/%q: %s %v, want %s", c.role, c.profile, got, err, c.want)
		}
	}
	if _, err := StoreFor(RoleGateway, "nope"); err == nil {
		t.Error("an unknown profile chose a store")
	}
}

func TestTheBidderStoreIsCopiedBeforeAnAssetChangingRestart(t *testing.T) {
	if NeedsBackup(Assets{Kcp: "k1", Kine: "n1"}, Assets{Kcp: "k1", Kine: "n1"}) {
		t.Fatal("a same-asset restart copies the store")
	}
	if !NeedsBackup(Assets{Kcp: "k1", Kine: "n1"}, Assets{Kcp: "k2", Kine: "n1"}) ||
		!NeedsBackup(Assets{Kcp: "k1", Kine: "n1"}, Assets{Kcp: "k1", Kine: "n2"}) {
		t.Fatal("an asset-changing restart does not copy the store")
	}
	dir := t.TempDir()
	if _, err := BackupSqlite(dir, time.Unix(1, 0)); err == nil {
		t.Fatal("a backup of a missing kine.db succeeded")
	}
	os.WriteFile(filepath.Join(dir, "kine.db"), []byte("db"), 0o600)
	os.WriteFile(filepath.Join(dir, "kine.db-wal"), []byte("wal"), 0o600)
	at := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	out, err := BackupSqlite(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if out != filepath.Join(dir, "backups", "20261012T030000Z") {
		t.Fatalf("backup dir %s", out)
	}
	for f, want := range map[string]string{"kine.db": "db", "kine.db-wal": "wal"} {
		b, err := os.ReadFile(filepath.Join(out, f))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", f, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "kine.db-shm")); !os.IsNotExist(err) {
		t.Fatalf("an absent -shm was invented: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "kine.db"), nil, 0o600)
	if _, err := BackupSqlite(dir, at.Add(time.Second)); err == nil {
		t.Fatal("an empty kine.db was backed up")
	}
}
