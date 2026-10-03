package clusterstack

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func openBaoDSN() string {
	return "postgres://postgres:secret@127.0.0.1:20001/" + OpenBaoDatabase + "?sslmode=disable"
}

func openBaoSpec() Spec {
	dir := "/var/lib/socialweb/clusters/a"
	return Spec{
		DataDir:            dir,
		OpenBaoConfig:      OpenBaoConfigPath(dir),
		OpenBaoPort:        20004,
		OpenBaoTLSCertFile: OpenBaoTLSCertPath(dir),
		OpenBaoTLSKeyFile:  OpenBaoTLSKeyPath(dir),
		OpenBaoPostgresDSN: openBaoDSN(),
	}
}

func parseHCL(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	var block []string
	for i, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
		case strings.HasSuffix(line, "{"):
			head := strings.Fields(strings.TrimSpace(strings.TrimSuffix(line, "{")))
			if len(head) != 2 || !strings.HasPrefix(head[1], "\"") {
				t.Fatalf("line %d of the rendered config is not a labeled block: %q", i+1, line)
			}
			block = append(block, head[0]+"."+strings.Trim(head[1], "\""))
		case line == "}":
			if len(block) == 0 {
				t.Fatalf("line %d closes a block that was never opened: %q", i+1, line)
			}
			block = block[:len(block)-1]
		default:
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				t.Fatalf("line %d is neither a block nor an assignment: %q", i+1, line)
			}
			path := strings.TrimSpace(key)
			if len(block) > 0 {
				path = strings.Join(block, ".") + "." + path
			}
			if _, dup := out[path]; dup {
				t.Fatalf("line %d sets %s a second time, so the config has two values where it "+
					"needs one", i+1, path)
			}
			out[path] = strings.Trim(strings.TrimSpace(value), "\"")
		}
	}
	if len(block) != 0 {
		t.Fatalf("the rendered config ends inside %v, so bao would refuse it as unparsable", block)
	}
	return out
}

func rendered(t *testing.T, s Spec) map[string]string {
	t.Helper()
	body, err := s.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig(%+v): %v", s, err)
	}
	return parseHCL(t, body)
}

func TestOpenBaoConfigListensOnTheLeasedPortAndOnlyOnLoopback(t *testing.T) {
	s := openBaoSpec()
	got := rendered(t, s)

	address, ok := got["listener.tcp.address"]
	if !ok {
		t.Fatalf("the config names no listener address, so bao would open its own default on the "+
			"whole host: %v", got)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("the listener address %q is not host:port: %v", address, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("the listener binds %q: the vault holds the identity bundle and the process "+
			"group is gateway-side, so nothing off this host dials it", host)
	}
	if port != "20004" {
		t.Fatalf("the listener binds port %s, and the lease the cluster holds is 20004: a config "+
			"that names a port the cluster has not leased is a port another cluster's lease "+
			"already covers", port)
	}
	if s.OpenBaoPort == 8200 {
		t.Fatalf("the fixture uses bao's own default port, so this test could pass on a config " +
			"that never read OpenBaoPort at all")
	}
	if got["listener.tcp.tls_cert_file"] != OpenBaoTLSCertPath(s.DataDir) {
		t.Fatalf("the listener carries no per-cluster server certificate. The single loopback "+
			"listener is TLS, and a guest dials the gateway address through pasta while the admin "+
			"dials loopback, so a listener with no certificate can answer neither: %v", got)
	}
	if got["listener.tcp.tls_key_file"] != OpenBaoTLSKeyPath(s.DataDir) {
		t.Fatalf("the listener carries no per-cluster server key: %v", got)
	}
	if _, set := got["listener.tcp.tls_disable"]; set {
		t.Fatalf("the config sets tls_disable: the listener is TLS in both directions, and a "+
			"tls_disable here would put the plaintext listener back: %v", got)
	}
}

func TestOpenBaoConfigRootsEveryPathInThisClustersDataDir(t *testing.T) {
	s := openBaoSpec()
	got := rendered(t, s)

	paths := 0
	for key, value := range got {
		if !filepath.IsAbs(value) {
			continue
		}
		paths++
		if !strings.HasPrefix(value, s.DataDir+string(filepath.Separator)) {
			t.Fatalf("%s is %q, which is outside this cluster's data dir %q: every path in this "+
				"file must belong to one cluster, or two clusters share a vault store and the "+
				"second to start reads the first one's seal", key, value, s.DataDir)
		}
	}
	if paths == 0 {
		t.Fatal("the config names no absolute path at all, so this test proved nothing about them")
	}
}

func TestOpenBaoConfigNamesThePostgresBackendAndNoFileStore(t *testing.T) {
	s := openBaoSpec()
	body, err := s.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig: %v", err)
	}
	if strings.Contains(body, `storage "file"`) {
		t.Fatalf("the config carries the file backend, which the architecture replaced with the "+
			"per-cluster postgres and which 2.6.2 logs as deprecated by v2.7.0:\n%s", body)
	}
	got := parseHCL(t, body)
	store, ok := got["storage.postgresql.connection_url"]
	if !ok {
		t.Fatalf("the config names no postgresql connection_url, so bao would refuse the store "+
			"the architecture decided on:\n%s", body)
	}
	if store != openBaoDSN() {
		t.Fatalf("the connection_url is %q, want the per-cluster DSN %q: a defaulted store is a "+
			"vault on a store that is not its own", store, openBaoDSN())
	}
	if got["storage.file.path"] != "" {
		t.Fatalf("the config still names a file storage path alongside the postgres backend: %v", got)
	}
}

func TestOpenBaoConfigRefusesAStoreThatIsNotItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		dsn  string
	}{
		{"no store", ""},
		{"kine's database", "postgres://postgres:secret@127.0.0.1:20001/k3s?sslmode=disable"},
		{"another cluster's database", "postgres://postgres:secret@127.0.0.1:20001/other?sslmode=disable"},
		{"no password", "postgres://postgres@127.0.0.1:20001/openbao?sslmode=disable"},
		{"no port", "postgres://postgres:secret@127.0.0.1/openbao?sslmode=disable"},
		{"not postgres", "mysql://postgres:secret@127.0.0.1:20001/openbao"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openBaoSpec()
			s.OpenBaoPostgresDSN = tc.dsn
			body, err := s.OpenBaoServerConfig()
			if err == nil {
				t.Fatalf("a vault whose store is %q rendered a config:\n%s", tc.dsn, body)
			}
			if !errors.Is(err, ErrNoOpenBaoStore) && !errors.Is(err, ErrOpenBaoStoreNotPerCluster) {
				t.Fatalf("the refusal for %q is not a named store error: %v", tc.dsn, err)
			}
			if body != "" {
				t.Fatalf("a refused render returned a body: %q", body)
			}
		})
	}
}

func TestOpenBaoConfigApiAddressIsTheListener(t *testing.T) {
	got := rendered(t, openBaoSpec())

	api, ok := got["api_addr"]
	if !ok {
		t.Fatalf("the config names no api_addr, so bao has no address to publish: %v", got)
	}
	want := "https://" + got["listener.tcp.address"]
	if api != want {
		t.Fatalf("api_addr is %q and the listener is %q: an api_addr that does not match the "+
			"listener is an address bao hands to a client that cannot reach it",
			api, got["listener.tcp.address"])
	}
}

func TestOpenBaoConfigIsOneIsolatedVaultWithNoUI(t *testing.T) {
	got := rendered(t, openBaoSpec())

	if got["disable_clustering"] != "true" {
		t.Fatalf("clustering is not disabled. A cluster-capable vault needs cluster_addr, whose "+
			"default is 8201 -- one port for the whole host -- and two clusters' vaults on one host "+
			"can join each other into one vault with two stores: %v", got)
	}
	if got["ui"] != "false" {
		t.Fatalf("the UI is not disabled, so a loopback port serves the vault's UI: %v", got)
	}
	if len(got) == 0 {
		t.Fatal("the renderer produced no settings at all")
	}
}

func TestOpenBaoConfigOmitsDisableMlockWhich262DoesNotImplement(t *testing.T) {
	body, err := openBaoSpec().OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig: %v", err)
	}
	if strings.Contains(body, "mlock") {
		t.Fatalf("the config names mlock. MEASURED on v2.6.2: 'disable_mlock' is an unsupported "+
			"field -- bao logs [WARN] unknown or unsupported field disable_mlock found in "+
			"configuration and starts anyway, because 2.6.2 does not mlock at all. Writing it adds "+
			"a permanent warning to every vault's log and tells a reader that mlock is implemented "+
			"somewhere; the container-less host is served by mlock being off by default, which is "+
			"what the field would have asked for. Config:\n%s", body)
	}
}

func TestTheVaultPortIsNeverDefaulted(t *testing.T) {
	s := openBaoSpec()
	s.OpenBaoPort = 0
	body, err := s.OpenBaoServerConfig()
	if !errors.Is(err, ErrNoOpenBaoPort) {
		t.Fatalf("a spec with no vault port rendered a config: err=%v, body=%q", err, body)
	}
	if body != "" {
		t.Fatalf("a refused render returned a body: %q", body)
	}

	got, err := openBaoSpec().OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig: %v", err)
	}
	if strings.Contains(got, "8200") {
		t.Fatalf("the config names bao's built-in 8200, the one port every vault that names no "+
			"port would take: %q", got)
	}
	if !strings.Contains(got, "20004") {
		t.Fatalf("the config does not name the leased port 20004: %q", got)
	}
}

func TestOpenBaoConfigRefusesAPathThatIsNotThisClusters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dataDir string
		config  string
	}{
		{name: "one file for every cluster", dataDir: "/var/lib/socialweb/clusters/a", config: "/etc/socialweb/openbao.hcl"},
		{name: "the data dir itself", dataDir: "/var/lib/socialweb/clusters/a", config: "/var/lib/socialweb/clusters/a"},
		{name: "a sibling that shares a prefix", dataDir: "/var/lib/socialweb/clusters/a", config: "/var/lib/socialweb/clusters/ab/openbao.hcl"},
		{name: "above the data dir", dataDir: "/var/lib/socialweb/clusters/a", config: "/var/lib/socialweb/clusters/openbao.hcl"},
		{name: "a relative path", dataDir: "/var/lib/socialweb/clusters/a", config: "openbao.hcl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openBaoSpec()
			s.DataDir = tc.dataDir
			s.OpenBaoConfig = tc.config
			if _, err := s.OpenBaoServerConfig(); !errors.Is(err, ErrOpenBaoConfigNotPerCluster) {
				t.Fatalf("config %q under data dir %q was accepted: %v", tc.config, tc.dataDir, err)
			}
		})
	}
}

func TestOpenBaoConfigRefusesARelativeDataDir(t *testing.T) {
	s := openBaoSpec()
	s.DataDir = "clusters/a"
	s.OpenBaoConfig = OpenBaoConfigPath(s.DataDir)
	if _, err := s.OpenBaoServerConfig(); !errors.Is(err, ErrOpenBaoDataDirNotAbsolute) {
		t.Fatalf("a relative data dir was accepted: %v", err)
	}
}

func TestTwoClustersRenderTwoDifferentVaults(t *testing.T) {
	a := openBaoSpec()
	b := openBaoSpec()
	b.DataDir = "/var/lib/socialweb/clusters/b"
	b.OpenBaoConfig = OpenBaoConfigPath(b.DataDir)
	b.OpenBaoTLSCertFile = OpenBaoTLSCertPath(b.DataDir)
	b.OpenBaoTLSKeyFile = OpenBaoTLSKeyPath(b.DataDir)
	b.OpenBaoPort = 20006
	b.OpenBaoPostgresDSN = "postgres://postgres:secret@127.0.0.1:20006/" + OpenBaoDatabase + "?sslmode=disable"

	first, err := a.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("cluster a: %v", err)
	}
	second, err := b.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("cluster b: %v", err)
	}
	if first == second {
		t.Fatalf("two clusters rendered one config:\n%s", first)
	}
	if strings.Contains(second, a.DataDir) {
		t.Fatalf("cluster b's config names cluster a's data dir:\n%s", second)
	}
	if strings.Contains(first, b.DataDir) {
		t.Fatalf("cluster a's config names cluster b's data dir:\n%s", first)
	}
}

func TestOpenBaoPathsArePerCluster(t *testing.T) {
	dir := "/var/lib/socialweb/clusters/a"
	for _, tc := range []struct {
		got  string
		want string
	}{
		{OpenBaoDir(dir), "/var/lib/socialweb/clusters/a/openbao"},
		{OpenBaoStorageDir(dir), "/var/lib/socialweb/clusters/a/openbao/data"},
		{OpenBaoConfigPath(dir), "/var/lib/socialweb/clusters/a/openbao.hcl"},
	} {
		if tc.got != tc.want {
			t.Fatalf("path is %q, want %q", tc.got, tc.want)
		}
		if !strings.HasPrefix(tc.got, dir+string(filepath.Separator)) {
			t.Fatalf("%q is not under this cluster's data dir %q, so a second cluster would reach "+
				"it", tc.got, dir)
		}
		if tc.got == dir {
			t.Fatalf("the path IS the data dir")
		}
	}
	if OpenBaoStorageDir("/var/lib/socialweb/clusters/a") == OpenBaoStorageDir("/var/lib/socialweb/clusters/b") {
		t.Fatal("two clusters are handed one storage dir")
	}
	if OpenBaoConfigPath("/var/lib/socialweb/clusters/a") == OpenBaoConfigPath("/var/lib/socialweb/clusters/b") {
		t.Fatal("two clusters are handed one config file, so whichever reconciles last decides " +
			"the other cluster's vault port")
	}
}
