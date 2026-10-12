package kcpstore

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTheBidderKcpStoreIsOneKineOnSqliteAndNoPostgres(t *testing.T) {
	spec, err := KineSqliteSpec(KineConfig{Unit: "socialweb-kcp-abcd1234-kine@a.service", Slice: "socialweb-kcp-abcd1234.slice",
		Exec: "/opt/kine", Dir: "/s/kcp/kine-a", Socket: "/run/user/1000/swc/abcd1234/kine-a.sock"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(spec.Exec, " ")
	if !strings.Contains(argv, "--endpoint sqlite:///s/kcp/kine-a/kine.db") || strings.Contains(argv, "postgres") {
		t.Fatalf("argv %s", argv)
	}
	if !slices.Contains(spec.Exec, "unix:///run/user/1000/swc/abcd1234/kine-a.sock") || !slices.Contains(spec.Exec, "--metrics-bind-address=0") ||
		strings.Contains(argv, "127.0.0.1") {
		t.Fatalf("kine must listen on its unix socket and serve no TCP port: %s", argv)
	}
}

func TestKineOnPostgresDialsTheStoresSocketAndListensOnAUnixSocket(t *testing.T) {
	spec, err := KinePostgresSpec(KineConfig{Unit: "socialweb-kcp-gw-kine@b.service", Slice: "socialweb-kcp-gw.slice",
		Exec: "/opt/kine", Dir: "/s/kcp/kine-b", Socket: "/s/kcp/run/kine-b.sock", PostgresSocketDir: "/s/kcp/run/pg"}, "socialweb-kcp-gw-pg.service")
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(spec.Exec, " ")
	if !strings.Contains(argv, "--endpoint postgres://kcp@localhost/kine?host=/s/kcp/run/pg&sslmode=disable") ||
		!strings.Contains(argv, "--listen-address unix:///s/kcp/run/kine-b.sock") || strings.Contains(argv, "127.0.0.1") {
		t.Fatalf("argv %s", argv)
	}
	if !slices.Equal(spec.Wants, []string{"socialweb-kcp-gw-pg.service"}) || !slices.Equal(spec.After, spec.Wants) {
		t.Fatalf("kine is After= and Wants= (never Requires=) its postgres: %+v", spec)
	}
	if _, err := KineSqliteArgv(KineConfig{Exec: "/opt/kine", Dir: "/s", Socket: "/" + strings.Repeat("x", 120)}); err == nil {
		t.Fatal("a kine socket path over the sun_path limit was accepted")
	}
}

func TestThePgUnitHasNoTcpListener(t *testing.T) {
	spec, err := PostgresSpec(PostgresConfig{Unit: "socialweb-kcp-gw-pg.service", Slice: "socialweb-kcp-gw.slice", BinDir: "/opt/pg/bin",
		Data: "/s/kcp/pg/data", SocketDir: "/s/kcp/run/pg"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(spec.Exec, " ")
	if spec.Exec[0] != "/opt/pg/bin/postgres" || !strings.Contains(argv, "-c listen_addresses=") || strings.Contains(argv, "listen_addresses=*") ||
		!strings.Contains(argv, "-c unix_socket_directories=/s/kcp/run/pg") || spec.KillSignal != syscall.SIGINT {
		t.Fatalf("spec %+v", spec)
	}
	if _, err := PostgresSpec(PostgresConfig{Unit: "socialweb-kcp-gw-pg.service", BinDir: "/opt/pg/bin", Data: "/d", SocketDir: "/" + strings.Repeat("y", 110)}); err == nil {
		t.Fatal("a socket directory whose socket path is over the sun_path limit was accepted")
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
		{RoleGateway, "quick", KindKineSqlite},
		{RoleGateway, "bed", KindKinePostgres},
		{RoleGateway, "prodlike", KindKinePostgres},
		{RoleGateway, "", KindKinePostgres},
		{RoleBidder, "bed", KindKineSqlite},
		{RoleBidder, "", KindKineSqlite},
	}
	for _, c := range cases {
		got, err := StoreFor(c.role, c.profile)
		if err != nil || got != c.want {
			t.Errorf("%s/%q: %s %v, want %s", c.role, c.profile, got, err, c.want)
		}
	}
	if _, err := StoreFor(RoleGateway, "minimal"); err == nil {
		t.Error("the minimal profile is gone; quick is the sqlite profile")
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
