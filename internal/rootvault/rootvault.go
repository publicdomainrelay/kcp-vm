package rootvault

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
)

const (
	DirName = "openbao"

	StorageDirName = "root"

	ConfigFileName = "root.hcl"

	TLSDirName = "tls"

	ServerCertName = "server.crt"
	ServerKeyName  = "server.key"

	ClientCertName = "client.crt"
	ClientKeyName  = "client.key"

	CAName = "ca.crt"

	UnsealKeysName = "unseal-keys.json"

	LoopbackHost = "127.0.0.1"

	Port = 8200

	configTemplate = `ui = false
disable_clustering = true
api_addr = "https://%[1]s"
listener "tcp" {
  address                            = "%[1]s"
  tls_disable                        = false
  tls_cert_file                      = "%[2]s"
  tls_key_file                       = "%[3]s"
  tls_client_ca_file                 = "%[4]s"
  tls_require_and_verify_client_cert = true
%[6]s}
storage "file" {
  path = "%[5]s"
}
`
)

var ErrStateDirEmpty = errors.New(
	"rootvault: StateDir is required. The root vault holds the cross-deployment credentials, and " +
		"every path in its config is derived from the state dir, so an empty one renders a config " +
		"whose storage path is the process's current directory: a vault started from one working " +
		"directory and restarted from another comes up on an empty store")

var ErrStateDirNotAbsolute = errors.New(
	"rootvault: StateDir must be absolute before a root-vault config can be rendered from it. A " +
		"relative one resolves against whichever directory the entrypoint happened to start in, so " +
		"the storage path in the rendered file names a directory nobody meant and the state volume " +
		"the credentials are meant to survive on is bypassed")

// ponytail: file backend, deprecated with an expiry at v2.7.0; the state-volume store moves to embedded-postgres when the root vault is migrated

func Address() string {
	return net.JoinHostPort(LoopbackHost, strconv.Itoa(Port))
}

func Dir(stateDir string) string {
	return filepath.Join(stateDir, DirName)
}

func StorageDir(stateDir string) string {
	return filepath.Join(Dir(stateDir), StorageDirName)
}

func ConfigPath(stateDir string) string {
	return filepath.Join(Dir(stateDir), ConfigFileName)
}

func TLSDir(stateDir string) string {
	return filepath.Join(Dir(stateDir), TLSDirName)
}

func TLSCAPath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), CAName)
}

func ServerCertPath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), ServerCertName)
}

func ServerKeyPath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), ServerKeyName)
}

func ClientCertPath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), ClientCertName)
}

func ClientKeyPath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), ClientKeyName)
}

func UnsealKeysPath(stateDir string) string {
	return filepath.Join(Dir(stateDir), UnsealKeysName)
}

type ServerOptions struct {
	UnauthedGenerateRoot bool
}

func ServerConfig(stateDir, addr string) (string, error) {
	return ServerConfigWith(stateDir, addr, ServerOptions{})
}

func ServerConfigWith(stateDir, addr string, o ServerOptions) (string, error) {
	if stateDir == "" {
		return "", ErrStateDirEmpty
	}
	if !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("%w (got %q)", ErrStateDirNotAbsolute, stateDir)
	}
	if addr == "" {
		addr = Address()
	}
	extra := ""
	if o.UnauthedGenerateRoot {
		extra = "  disable_unauthed_generate_root_endpoints = false\n"
	}
	return fmt.Sprintf(configTemplate, addr,
		ServerCertPath(stateDir), ServerKeyPath(stateDir), TLSCAPath(stateDir), StorageDir(stateDir), extra), nil
}
