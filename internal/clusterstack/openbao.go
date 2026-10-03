package clusterstack

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	OpenBaoDirName = "openbao"

	OpenBaoStorageDirName = "data"

	OpenBaoConfigFileName = "openbao.hcl"

	openBaoLoopbackHost = "127.0.0.1"

	OpenBaoDatabase = "openbao"

	openBaoConfigTemplate = `ui = false
disable_clustering = true
api_addr = "https://%[1]s"
listener "tcp" {
  address       = "%[1]s"
  tls_cert_file = "%[2]s"
  tls_key_file  = "%[3]s"
}
storage "postgresql" {
  connection_url = "%[4]s"
}
`
)

var ErrNoOpenBaoTLSCertificate = errors.New(
	"clusterstack: the vault listener is loopback-only and TLS, so it needs the per-cluster " +
		"server certificate and key. A listener without them comes up as an ACME/plaintext " +
		"listener: a guest that dials the gateway address through pasta, and the admin that " +
		"dials loopback, both need certificate auth to be presentable in the handshake")

var ErrNoOpenBaoPort = errors.New(
	"clusterstack: Spec.OpenBaoPort is required. bao takes no port on its command line, so a " +
		"config that names no address leaves the vault on its own built-in 8200 -- one port for " +
		"the whole host. The first cluster's vault binds it, every later cluster's vault dies at " +
		"bind, and the file that produced the failure is byte-identical for both clusters")

var ErrOpenBaoDataDirNotAbsolute = errors.New(
	"clusterstack: Spec.DataDir must be absolute before an OpenBao config can be rendered from " +
		"it. Every path in that config is derived from DataDir, so a relative one resolves against " +
		"whichever directory the gateway happened to start in: two clusters started from one " +
		"directory are handed one vault's storage and one vault's seal, and a gateway restarted " +
		"from another directory brings the vault up on an empty store")

var ErrOpenBaoConfigNotPerCluster = errors.New(
	"clusterstack: Spec.OpenBaoConfig must name a file inside Spec.DataDir. A config shared " +
		"between clusters is written by whichever cluster reconciled last, so the listener port " +
		"and the storage path in it are that cluster's: the other cluster's vault comes up holding " +
		"a port its own leases do not cover and a store that is not its own")

var ErrNoOpenBaoStore = errors.New(
	"clusterstack: Spec.OpenBaoPostgresDSN is required. The vault's store is this cluster's own " +
		"embedded-postgres, never a defaulted one: a vault that names no store, or one shared with " +
		"another cluster, comes up on a store that is not its own, and the store holds the vault's " +
		"seal and its data, so the wrong store is a vault that cannot unseal the material it holds")

var ErrOpenBaoStoreNotPerCluster = errors.New(
	"clusterstack: Spec.OpenBaoPostgresDSN must name this cluster's own postgres database " +
		"(\"" + OpenBaoDatabase + "\"), with its own host, port and credential. A store shared with " +
		"the objects kine writes is one database two owners write to, and a store that is another " +
		"cluster's is a vault whose unseal material does not belong to it")

func OpenBaoDir(dataDir string) string {
	return filepath.Join(dataDir, OpenBaoDirName)
}

func OpenBaoStorageDir(dataDir string) string {
	return filepath.Join(OpenBaoDir(dataDir), OpenBaoStorageDirName)
}

func OpenBaoConfigPath(dataDir string) string {
	return filepath.Join(dataDir, OpenBaoConfigFileName)
}

func (s Spec) openBaoAddress() (string, error) {
	if s.OpenBaoPort <= 0 || s.OpenBaoPort > 65535 {
		return "", fmt.Errorf("%w (got %d)", ErrNoOpenBaoPort, s.OpenBaoPort)
	}
	return net.JoinHostPort(openBaoLoopbackHost, strconv.Itoa(s.OpenBaoPort)), nil
}

func isPerCluster(dataDir, path string) bool {
	rel, err := filepath.Rel(dataDir, path)
	if err != nil {
		return false
	}
	if filepath.IsAbs(rel) || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s Spec) OpenBaoServerConfig() (string, error) {
	if !filepath.IsAbs(s.DataDir) {
		return "", fmt.Errorf("%w (got %q)", ErrOpenBaoDataDirNotAbsolute, s.DataDir)
	}
	if !isPerCluster(s.DataDir, s.OpenBaoConfig) {
		return "", fmt.Errorf("%w (config %q, data dir %q)", ErrOpenBaoConfigNotPerCluster,
			s.OpenBaoConfig, s.DataDir)
	}
	address, err := s.openBaoAddress()
	if err != nil {
		return "", err
	}
	if s.OpenBaoTLSCertFile == "" || s.OpenBaoTLSKeyFile == "" {
		return "", fmt.Errorf("%w (cert %q, key %q)", ErrNoOpenBaoTLSCertificate,
			s.OpenBaoTLSCertFile, s.OpenBaoTLSKeyFile)
	}
	dsn, err := openBaoStoreDSN(s.OpenBaoPostgresDSN)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(openBaoConfigTemplate, address, s.OpenBaoTLSCertFile, s.OpenBaoTLSKeyFile,
		dsn), nil
}

func openBaoStoreDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", ErrNoOpenBaoStore
	}
	if strings.ContainsAny(dsn, "\"\n\r") {
		return "", fmt.Errorf("%w (%q carries a character that would end the HCL string)",
			ErrOpenBaoStoreNotPerCluster, dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("%w (parsing %q: %v)", ErrOpenBaoStoreNotPerCluster, dsn, err)
	}
	switch u.Scheme {
	case "postgres", "postgresql":
	default:
		return "", fmt.Errorf("%w (scheme %q, want postgres)", ErrOpenBaoStoreNotPerCluster, u.Scheme)
	}
	if u.Hostname() == "" || u.Port() == "" {
		return "", fmt.Errorf("%w (%q names no host:port)", ErrOpenBaoStoreNotPerCluster, dsn)
	}
	if u.User == nil {
		return "", fmt.Errorf("%w (%q carries no user)", ErrOpenBaoStoreNotPerCluster, dsn)
	}
	if _, ok := u.User.Password(); !ok {
		return "", fmt.Errorf("%w (%q carries no password)", ErrOpenBaoStoreNotPerCluster, dsn)
	}
	if strings.TrimPrefix(u.Path, "/") != OpenBaoDatabase {
		return "", fmt.Errorf("%w (%q names database %q)", ErrOpenBaoStoreNotPerCluster, dsn,
			strings.TrimPrefix(u.Path, "/"))
	}
	return dsn, nil
}
