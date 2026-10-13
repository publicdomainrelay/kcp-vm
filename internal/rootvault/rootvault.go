package rootvault

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
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

	ClientsCAName = "clients-ca.crt"

	ClientsCAKeyName = "clients-ca.key"

	ClientCABundleName = "client-cas.crt"

	ClientsDirName = "clients"

	UnsealKeysName = "unseal-keys.json"

	LoopbackHost = "127.0.0.1"

	Port = 8200
)

var ErrConfigValue = errors.New("rootvault: a config value carries a quote, a backslash or a line break and would end its HCL string")

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

func ClientsCAPath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), ClientsCAName)
}

func ClientsCAKeyPath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), ClientsCAKeyName)
}

func ClientCABundlePath(stateDir string) string {
	return filepath.Join(TLSDir(stateDir), ClientCABundleName)
}

func AppClientCertPath(stateDir, name string) string {
	return filepath.Join(Dir(stateDir), ClientsDirName, name, ClientCertName)
}

func AppClientKeyPath(stateDir, name string) string {
	return filepath.Join(Dir(stateDir), ClientsDirName, name, ClientKeyName)
}

func UnsealKeysPath(stateDir string) string {
	return filepath.Join(Dir(stateDir), UnsealKeysName)
}

type ServerOptions struct {
	UnauthedGenerateRoot bool

	ClientCAFile string

	CertFile string

	KeyFile string

	ClientCertOptional bool

	PostgresDSN string

	PluginDirectory string
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
	cert, key := ServerCertPath(stateDir), ServerKeyPath(stateDir)
	if o.CertFile != "" || o.KeyFile != "" {
		cert, key = o.CertFile, o.KeyFile
	}
	clientCA := TLSCAPath(stateDir)
	if o.ClientCAFile != "" {
		clientCA = o.ClientCAFile
	}
	for _, v := range []string{addr, cert, key, clientCA, o.PostgresDSN, o.PluginDirectory} {
		if strings.ContainsAny(v, "\"\\\n\r") {
			return "", fmt.Errorf("%w (%q)", ErrConfigValue, v)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ui = false\ndisable_clustering = true\napi_addr = \"https://%s\"\n", addr)
	if o.PluginDirectory != "" {
		fmt.Fprintf(&b, "plugin_directory = \"%s\"\n", o.PluginDirectory)
	}
	b.WriteString("listener \"tcp\" {\n")
	fmt.Fprintf(&b, "  address                            = \"%s\"\n", addr)
	b.WriteString("  tls_disable                        = false\n")
	fmt.Fprintf(&b, "  tls_cert_file                      = \"%s\"\n", cert)
	fmt.Fprintf(&b, "  tls_key_file                       = \"%s\"\n", key)
	if !o.ClientCertOptional {
		fmt.Fprintf(&b, "  tls_client_ca_file                 = \"%s\"\n", clientCA)
		b.WriteString("  tls_require_and_verify_client_cert = true\n")
	}
	if o.UnauthedGenerateRoot {
		b.WriteString("  disable_unauthed_generate_root_endpoints = false\n")
	}
	b.WriteString("}\n")
	if o.PostgresDSN != "" {
		fmt.Fprintf(&b, "storage \"postgresql\" {\n  connection_url = \"%s\"\n}\n", o.PostgresDSN)
	} else {
		fmt.Fprintf(&b, "storage \"file\" {\n  path = \"%s\"\n}\n", StorageDir(stateDir))
	}
	return b.String(), nil
}
