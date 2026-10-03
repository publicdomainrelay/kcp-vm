//go:build linux

package clusterprovider

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

const testPortAttempts = 8

var testRange, durabilityRange = postgresWindows()

func postgresWindows() (portallocator.Range, portallocator.Range) {
	slot, err := liveports.TakeForProcess(liveports.PostgresWindows)
	if err != nil {
		panic(err)
	}
	mid := slot.First + (slot.Last-slot.First+1)/2 - 1
	return portallocator.Range{First: slot.First, Last: mid},
		portallocator.Range{First: mid + 1, Last: slot.Last}
}

func skipWithoutPostgres(t *testing.T) {
	t.Helper()
	livegate.Short(t, "this test starts a real postgres and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	for _, bin := range []string{"initdb", "postgres", "psql"} {
		if _, err := exec.LookPath(bin); err != nil {
			livegate.RequireLocal(t, "%s is not on PATH: the launcher's install is absent, so there is nothing to exercise", bin)
		}
	}
}

func testAllocator(t *testing.T) *portallocator.Allocator {
	t.Helper()
	return testAllocatorIn(t, testRange)
}

func testNetworks(t *testing.T) *subnetallocator.Allocator {
	t.Helper()
	nets, err := subnetallocator.Open(subnetallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "networks.json"),
	})
	if err != nil {
		t.Fatalf("subnetallocator.Open: %v", err)
	}
	return nets
}

func testAllocatorIn(t *testing.T, rng portallocator.Range) *portallocator.Allocator {
	t.Helper()
	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
		Range:     rng,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	return alloc
}

func startTestPostgres(t *testing.T, cluster string) (*Postgres, *portallocator.Allocator) {
	t.Helper()
	alloc := testAllocator(t)
	return startTestPostgresOn(t, alloc, cluster), alloc
}

func startTestPostgresOn(t *testing.T, alloc *portallocator.Allocator, cluster string) *Postgres {
	t.Helper()
	skipWithoutPostgres(t)

	var lastErr error
	for attempt := 0; attempt < testPortAttempts; attempt++ {
		lease, err := alloc.Allocate(portallocator.Request{Cluster: cluster, Purpose: portallocator.PurposePostgres})
		if err != nil {
			t.Fatalf("allocate PurposePostgres: %v", err)
		}
		pg, err := StartPostgres(context.Background(), PostgresSpec{
			Cluster:  cluster,
			StateDir: t.TempDir(),
			Port:     lease.Port,
			Log:      discard(),
		})
		if err == nil {
			t.Cleanup(func() {
				if err := pg.Stop(context.Background()); err != nil {
					t.Errorf("CLEANUP: stopping %s's postgres: %v", cluster, err)
				}
			})
			return pg
		}
		lastErr = err
		if err := alloc.ReleasePurpose(cluster, portallocator.PurposePostgres); err != nil {
			t.Fatalf("releasing the port %d whose postgres did not start: %v", lease.Port, err)
		}
	}
	t.Fatalf("StartPostgres did not find a bindable port in %d attempts: %v. The lease file is "+
		"per-process, so two test binaries of this package lease the same first port from their "+
		"own files and one of them loses the bind -- the probe cannot help, because it runs before "+
		"the peer's postmaster has bound", testPortAttempts, lastErr)
	return nil
}

func sql(t *testing.T, pg *Postgres, stmt string) string {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Fatalf("psql: %v", err)
	}
	out, err := pg.query(context.Background(), psql, stmt)
	if err != nil {
		t.Fatalf("psql %q: %v", stmt, err)
	}
	return out
}

func sqlIn(t *testing.T, pg *Postgres, database, stmt string) string {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Fatalf("psql: %v", err)
	}
	argv := []string{
		"-h", postgresLoopbackHost,
		"-p", strconv.Itoa(pg.spec.Port),
		"-U", PostgresUser,
		"-d", database,
		"-tAc", stmt,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, psql, argv...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+pg.password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql -d %s %q: %v: %s", database, stmt, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPostgresServesThisClustersDataDirectoryAndNothingElse(t *testing.T) {
	pg, _ := startTestPostgres(t, "adr0026a")

	got := sql(t, pg, "show data_directory")
	if filepath.Clean(got) != filepath.Clean(pg.DataDir()) {
		t.Fatalf("the server on port %d serves %s, this cluster's data dir is %s: a lease that "+
			"drifted onto another cluster's port is exactly the cross-tenant read ADR 0026 measured",
			pg.spec.Port, got, pg.DataDir())
	}
	if !pg.Up() {
		t.Fatal("Up() is false for a server this test just started; it is the predicate " +
			"procgroup.Options.PostgresUp takes, and a false here refuses openbao")
	}
}

func TestTwoClustersShareNeitherPortDataDirNorPassword(t *testing.T) {
	alloc := testAllocator(t)
	a := startTestPostgresOn(t, alloc, "tenant-a")
	b := startTestPostgresOn(t, alloc, "tenant-b")

	if a.spec.Port == b.spec.Port {
		t.Fatalf("two clusters were given port %d", a.spec.Port)
	}
	if filepath.Clean(a.DataDir()) == filepath.Clean(b.DataDir()) {
		t.Fatalf("two clusters share the data dir %s", a.DataDir())
	}
	if a.password == b.password {
		t.Fatal("two clusters share a postgres password: a distinct port closes the DSN " +
			"collision and a distinct credential is what closes the port scan behind it")
	}
	if a.DSN() == b.DSN() {
		t.Fatalf("two clusters' DSNs are identical: %s", a.DSN())
	}
	if got := sql(t, a, "select count(*) from pg_database where datname='"+PostgresDatabase+"'"); got != "1" {
		t.Fatalf("cluster a's own %s database: count=%q, want 1", PostgresDatabase, got)
	}
	for name, pg := range map[string]*Postgres{"a": a, "b": b} {
		if got := sql(t, pg, "select count(*) from pg_database where datname='"+PostgresVaultDatabase+"'"); got != "1" {
			t.Fatalf("cluster %s has no vault database %s: count=%q", name, PostgresVaultDatabase, got)
		}
	}
}

func TestTheVaultGetsItsOwnDatabaseOnThisClustersPostgres(t *testing.T) {
	pg, _ := startTestPostgres(t, "vaultdb")

	if PostgresVaultDatabase == PostgresDatabase {
		t.Fatalf("the vault database and kine's database are both %q: the vault's tables and the "+
			"objects kine writes would share one database", PostgresDatabase)
	}
	if got := sql(t, pg, "select count(*) from pg_database where datname='"+PostgresVaultDatabase+"'"); got != "1" {
		t.Fatalf("the vault database %s was not created alongside %s: count=%q",
			PostgresVaultDatabase, PostgresDatabase, got)
	}

	kine, err := url.Parse(pg.DSN())
	if err != nil {
		t.Fatalf("parsing kine's DSN: %v", err)
	}
	vault, err := url.Parse(pg.VaultDSN())
	if err != nil {
		t.Fatalf("parsing the vault's DSN: %v", err)
	}
	if got := strings.TrimPrefix(kine.Path, "/"); got != PostgresDatabase {
		t.Fatalf("kine's DSN names database %q, want %q", got, PostgresDatabase)
	}
	if got := strings.TrimPrefix(vault.Path, "/"); got != PostgresVaultDatabase {
		t.Fatalf("the vault's DSN names database %q, want %q", got, PostgresVaultDatabase)
	}
	for _, u := range []*url.URL{kine, vault} {
		if u.Hostname() != postgresLoopbackHost || u.Port() != strconv.Itoa(pg.spec.Port) {
			t.Fatalf("a DSN names %s:%s, want this cluster's %s:%d: a vault on another cluster's "+
				"store cannot unseal its own material", u.Hostname(), u.Port(),
				postgresLoopbackHost, pg.spec.Port)
		}
		if _, ok := u.User.Password(); !ok {
			t.Fatalf("a DSN carries no password: %s", u.Redacted())
		}
	}
}

func TestTheStoreSurvivesTheLauncher(t *testing.T) {
	skipWithoutPostgres(t)

	stateDir := t.TempDir()
	alloc, err := portallocator.Open(portallocator.Config{
		LeasePath: filepath.Join(t.TempDir(), "leases.json"),
		Range:     testRange,
	})
	if err != nil {
		t.Fatalf("portallocator.Open: %v", err)
	}
	lease, err := alloc.Allocate(portallocator.Request{Cluster: "durable", Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	spec := PostgresSpec{Cluster: "durable", StateDir: stateDir, Port: lease.Port, Log: discard()}

	first, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("first StartPostgres: %v", err)
	}
	dsn, password := first.DSN(), first.password
	sql(t, first, `CREATE TABLE IF NOT EXISTS survive (k text primary key, v text)`)
	sql(t, first, `INSERT INTO survive VALUES ('object','still here')`)
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if first.Up() {
		t.Fatal("Up() is true after Stop, so the predicate would pass a dead store")
	}

	second, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("second StartPostgres (the re-provision): %v", err)
	}
	defer func() {
		if err := second.Stop(context.Background()); err != nil {
			t.Errorf("CLEANUP: %v", err)
		}
	}()

	if got := sql(t, second, `SELECT v FROM survive WHERE k='object'`); got != "still here" {
		t.Fatalf("the object did not survive the launcher: got %q", got)
	}
	if second.DSN() != dsn || second.password != password {
		t.Fatalf("the DSN changed across a restart:\n first: %s\nsecond: %s", dsn, second.DSN())
	}
	if got := sql(t, second, "select count(*) from pg_database where datname='"+PostgresDatabase+"'"); got != "1" {
		t.Fatalf("%s database count=%q after a restart", PostgresDatabase, got)
	}
}

func TestStartPostgresAdoptsARunningServer(t *testing.T) {
	pg, _ := startTestPostgres(t, "adopt")
	first := pg.cmd.Process.Pid

	again, err := StartPostgres(context.Background(), pg.spec)
	if err != nil {
		t.Fatalf("StartPostgres while one is running: %v", err)
	}
	if again.cmd != nil {
		t.Fatal("a second server was started rather than the running one adopted; postgres " +
			"would then die on its own postmaster.pid")
	}
	if !again.Up() {
		t.Fatal("the adopted server does not report Up")
	}
	if got := sql(t, again, "select pg_backend_pid() <> 0"); got != "t" {
		t.Fatalf("adopted handle cannot query: %q", got)
	}
	if _, err := os.Stat(filepath.Join(pg.DataDir(), "postmaster.pid")); err != nil {
		t.Fatalf("the running server's postmaster.pid is gone: %v", err)
	}
	t.Logf("adopted pid %d in place", first)
}

func TestStartPostgresClearsAStalePidFileLeftByARecycledProcess(t *testing.T) {
	pg, _ := startTestPostgres(t, "stale-pid")
	spec := pg.spec
	if err := pg.Stop(context.Background()); err != nil {
		t.Fatalf("stopping the first server: %v", err)
	}
	pidPath := filepath.Join(spec.DataDir(), "postmaster.pid")
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a clean stop left %s behind: %v", pidPath, err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatalf("writing the stale pid file: %v", err)
	}
	again, err := StartPostgres(context.Background(), spec)
	if err != nil {
		t.Fatalf("StartPostgres over a postmaster.pid whose pid belongs to this test process, "+
			"as a container restart's recycled pid does: %v", err)
	}
	t.Cleanup(func() {
		if err := again.Stop(context.Background()); err != nil {
			t.Errorf("CLEANUP: stopping the server: %v", err)
		}
	})
	if !again.Up() {
		t.Fatal("the restarted server does not report Up")
	}
}

func TestTheDSNNamesTheLoopbackAddressTheOnlyClientIsOnThisHost(t *testing.T) {
	pg, _ := startTestPostgres(t, "reachable")

	dsn := pg.DSN()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("the DSN does not parse as a URL: %v: %s", err, dsn)
	}
	if u.Hostname() != postgresLoopbackHost {
		t.Fatalf("the DSN names host %q rather than %s: %s. The store is started by the gateway and its only "+
			"client is kine, a child process of the same host, so any other name is an address nothing here "+
			"answers on, kine never connects, and k3s never starts", u.Hostname(), postgresLoopbackHost, dsn)
	}
	if u.Port() != strconv.Itoa(pg.spec.Port) {
		t.Fatalf("the DSN names port %q rather than this cluster's lease %d: %s", u.Port(), pg.spec.Port, dsn)
	}
	if !strings.Contains(dsn, "/"+PostgresDatabase) {
		t.Fatalf("the DSN does not name the %s database: %s", PostgresDatabase, dsn)
	}
	if !strings.Contains(dsn, "sslmode=disable") {
		t.Fatalf("the DSN does not set sslmode: %s", dsn)
	}
	if got := psqlConnString(t, dsn, "select 1"); got != "1" {
		t.Fatalf("the DSN this store hands kine does not connect: psql printed %q", got)
	}
}

func TestTheStoresPgHbaAdmitsTheLoopbackRangeAndNothingWider(t *testing.T) {
	pg, _ := startTestPostgres(t, "hba")

	b, err := os.ReadFile(filepath.Join(pg.DataDir(), "pg_hba.conf"))
	if err != nil {
		t.Fatalf("read pg_hba.conf: %v", err)
	}
	lines := pgHbaHostLines(t, string(b))
	if len(lines) == 0 {
		t.Fatalf("pg_hba.conf carries no host line at all, so nothing below is a statement about this store:\n%s", b)
	}
	loopback := false
	for _, l := range lines {
		ip, _, err := net.ParseCIDR(l.address)
		if err != nil {
			t.Fatalf("pg_hba.conf line %q does not name a CIDR in %q", l.raw, l.address)
		}
		if !ip.IsLoopback() {
			t.Fatalf("pg_hba.conf admits %q from %s: %s. Every cluster's store holds every object of that "+
				"cluster, and nothing off this host ever dials it, so a range that reaches past the loopback "+
				"address admits a client that does not exist and opens the store to every one that does",
				l.address, l.database, l.raw)
		}
		if l.database == "all" && l.user == "all" && l.auth != "scram-sha-256" {
			t.Fatalf("pg_hba.conf admits %s from %s with %q: the loopback bind is defence in depth and not a "+
				"substitute for the credential, because any local process can reach the loopback address",
				l.database, l.address, l.auth)
		}
		if l.database == "all" && l.user == "all" && l.address == postgresLoopbackCIDR && l.auth == "scram-sha-256" {
			loopback = true
		}
	}
	if !loopback {
		t.Fatalf("pg_hba.conf has no `host all all %s scram-sha-256` line, so a client on the loopback "+
			"address is refused with `no pg_hba.conf entry for host \"%s\"`:\n%s",
			postgresLoopbackCIDR, postgresLoopbackHost, b)
	}
}

func TestTheStartedStoreListensOnLoopbackAndNowhereElse(t *testing.T) {
	pg, _ := startTestPostgres(t, "loopbackbind")

	argv := strings.Join(pg.cmd.Args, " ")
	want := "listen_addresses=" + postgresLoopbackHost
	if !strings.Contains(argv, want) {
		t.Fatalf("postgres was started without `-c %s`, so postgres applies its own default of every "+
			"interface: %s", want, argv)
	}
	for _, wider := range []string{"listen_addresses=*", "listen_addresses=0.0.0.0", "listen_addresses=::"} {
		if strings.Contains(argv, wider) {
			t.Fatalf("postgres was started with `-c %s`, which puts every cluster's store on every "+
				"interface of this host: %s", wider, argv)
		}
	}
	if !pg.Up() {
		t.Fatal("the readiness probe dials the loopback address and got nothing back, so narrowing the " +
			"bind refused the store this component just started")
	}
	others := nonLoopbackAddresses(t)
	if len(others) == 0 {
		livegate.RequireLocal(t, "this host carries no non-loopback IPv4 address, so there is nothing the store could have been reached on from elsewhere")
	}
	for _, addr := range others {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(pg.spec.Port)), time.Second)
		if err != nil {
			continue
		}
		_ = conn.Close()
		t.Fatalf("port %d answers on %s as well as on %s: every cluster's store is then reachable from "+
			"whatever shares this host's network, and the credential is the only thing left in the way",
			pg.spec.Port, addr, postgresLoopbackHost)
	}
	t.Logf("port %d answers on %s and on none of %v", pg.spec.Port, postgresLoopbackHost, others)
}

func TestNoFieldOnPostgresSpecCanExpressAnOffHostStore(t *testing.T) {
	spec := reflect.TypeOf(PostgresSpec{})

	for _, banned := range []string{"host", "cidr", "addr", "guest", "network", "remote", "endpoint"} {
		for i := range spec.NumField() {
			field := spec.Field(i)
			if strings.Contains(strings.ToLower(field.Name), banned) {
				t.Fatalf("PostgresSpec.%s can express where the store is reachable. That shape IS the defect: "+
					"it let a caller name an address the store is not on, and every store is on the loopback "+
					"address of the host that started it", field.Name)
			}
		}
	}
	for i := range spec.NumField() {
		field := spec.Field(i)
		switch field.Type.Kind() {
		case reflect.Slice, reflect.Map, reflect.Array:
			t.Fatalf("PostgresSpec.%s is a %s: a collection is where a set of addresses the store admits "+
				"would live, and this store admits one, the loopback address", field.Name, field.Type)
		}
	}
	for _, field := range []string{"Cluster", "StateDir", "Port"} {
		if _, ok := spec.FieldByName(field); !ok {
			t.Fatalf("PostgresSpec has no %s field, so this test is reading a spec that no longer describes "+
				"the store it was written against", field)
		}
	}
	for _, gone := range []string{"GuestHostOrDefault", "GuestCIDROrDefault"} {
		if _, ok := spec.MethodByName(gone); ok {
			t.Fatalf("PostgresSpec.%s still exists, so the guest vocabulary that produced the unreachable "+
				"DSN is only half removed", gone)
		}
	}
}

func TestTheStoreOpensNoUnixSocketEvenAtADeepStateDir(t *testing.T) {
	skipWithoutPostgres(t)

	deep := t.TempDir()
	for len(filepath.Join(deep, "state")) <= 160 {
		deep = filepath.Join(deep, "perclusterscopedir")
	}
	deep = filepath.Join(deep, "state")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", deep, err)
	}

	alloc := testAllocatorIn(t, durabilityRange)
	lease, err := alloc.Allocate(portallocator.Request{Cluster: "deepsocket", Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("allocate PurposePostgres: %v", err)
	}
	pg, err := StartPostgres(context.Background(), PostgresSpec{
		Cluster:  "deepsocket",
		StateDir: deep,
		Port:     lease.Port,
		Log:      discard(),
	})
	if err != nil {
		t.Fatalf("a state dir %d bytes deep (%s) must not be a store that cannot start: %v",
			len(deep), deep, err)
	}
	defer func() {
		if err := pg.Stop(context.Background()); err != nil {
			t.Errorf("CLEANUP: %v", err)
		}
	}()

	if got := sql(t, pg, "show unix_socket_directories"); got != "" {
		t.Fatalf("the server keeps a unix socket directory (%q): a socket under a per-cluster state "+
			"dir hits the sun_path cap (107 bytes on this host, 103 on darwin, where the first "+
			"measurement was taken), which fails on exactly the deployments that are most careful "+
			"about where state lives. Every call must be TCP and scram-sha-256, with no "+
			"trust-authenticated local path at all", got)
	}
	var sockets []string
	walkErr := filepath.WalkDir(deep, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasPrefix(entry.Name(), ".s.PGSQL.") {
			sockets = append(sockets, path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking %s: %v", deep, walkErr)
	}
	if len(sockets) > 0 {
		t.Fatalf("socket files under the state dir: %v", sockets)
	}
}

func TestAStoreOnAPortAnotherClusterHoldsIsNotReportedReady(t *testing.T) {
	skipWithoutPostgres(t)

	alloc := testAllocatorIn(t, durabilityRange)
	lease, err := alloc.Allocate(portallocator.Request{Cluster: "port-holder", Purpose: portallocator.PurposePostgres})
	if err != nil {
		t.Fatalf("allocate PurposePostgres: %v", err)
	}

	holderSpec := PostgresSpec{Cluster: "port-holder", StateDir: t.TempDir(), Port: lease.Port, Log: discard()}
	holder, err := StartPostgres(context.Background(), holderSpec)
	if err != nil {
		t.Fatalf("StartPostgres: %v", err)
	}
	defer func() {
		if err := holder.Stop(context.Background()); err != nil {
			t.Errorf("CLEANUP: %v", err)
		}
	}()

	secondSpec := PostgresSpec{Cluster: "second", StateDir: t.TempDir(), Port: lease.Port, Log: discard()}
	started := time.Now()
	second, err := StartPostgres(context.Background(), secondSpec)
	if err == nil {
		_ = second.Stop(context.Background())
		t.Fatalf("a store that cannot bind port %d was reported ready because %q's store answers "+
			"there: readiness is a bare dial rather than `show data_directory` naming THIS cluster's "+
			"own dir, so a store that is not this cluster's is what the join is handed",
			lease.Port, holderSpec.Cluster)
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Fatalf("the failure was reported as %q rather than as a postgres that exited: with no "+
			"cmd.Wait() in flight ProcessState stays nil, the exited check is dead code, and the "+
			"only way out is the %s deadline", err, postgresReadyTimeout)
	}
	if elapsed := time.Since(started); elapsed > postgresReadyTimeout/2 {
		t.Fatalf("the failure took %s, so it was the %s deadline rather than the process exiting",
			elapsed, postgresReadyTimeout)
	}
	if got := filepath.Clean(sql(t, holder, "show data_directory")); got != filepath.Clean(holderSpec.DataDir()) {
		t.Fatalf("the refused start disturbed the store holding the port: it serves %s rather than %s",
			got, holderSpec.DataDir())
	}
}

func psqlConnString(t *testing.T, conn, stmt string) string {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Fatalf("psql: %v", err)
	}
	out, err := exec.Command(psql, "-tAc", stmt, conn).CombinedOutput()
	if err != nil {
		t.Fatalf("psql %q against the DSN %q: %v: %s", stmt, conn, err, out)
	}
	return strings.TrimSpace(string(out))
}

type pgHbaHostLine struct {
	raw      string
	database string
	user     string
	address  string
	auth     string
}

func pgHbaHostLines(t *testing.T, contents string) []pgHbaHostLine {
	t.Helper()
	var out []pgHbaHostLine
	for _, raw := range strings.Split(contents, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "host" {
			continue
		}
		out = append(out, pgHbaHostLine{
			raw:      line,
			database: fields[1],
			user:     fields[2],
			address:  fields[3],
			auth:     fields[4],
		})
	}
	return out
}

func nonLoopbackAddresses(t *testing.T) []string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("net.InterfaceAddrs: %v", err)
	}
	var out []string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.To4() == nil || ipNet.IP.IsLoopback() {
			continue
		}
		out = append(out, ipNet.IP.String())
	}
	return out
}
