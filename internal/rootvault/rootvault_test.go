package rootvault

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const stateDir = "/var/lib/socialweb"

func TestServerConfigTurnsTLSOnAndDemandsAClientCertificate(t *testing.T) {
	body, err := ServerConfig(stateDir, Address())
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if strings.Contains(body, "tls_disable = true") {
		t.Fatalf("the root-vault listener is still plaintext:\n%s", body)
	}
	for _, want := range []string{
		"tls_disable",
		"tls_cert_file",
		"tls_key_file",
		"tls_client_ca_file",
		"tls_require_and_verify_client_cert = true",
		`api_addr = "https://127.0.0.1:8200"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the root-vault config does not carry %q, so its listener is not the mutual-TLS "+
				"one its consumers present a certificate to:\n%s", want, body)
		}
	}
}

func TestServerConfigRefusesAnEmptyOrRelativeStateDir(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stateDir string
		want     error
	}{
		{name: "empty", stateDir: "", want: ErrStateDirEmpty},
		{name: "relative", stateDir: "var/lib/socialweb", want: ErrStateDirNotAbsolute},
		{name: "bare relative", stateDir: ".", want: ErrStateDirNotAbsolute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := ServerConfig(tc.stateDir, Address())
			if !errors.Is(err, tc.want) {
				t.Fatalf("ServerConfig(%q): err=%v, want %v", tc.stateDir, err, tc.want)
			}
			if body != "" {
				t.Fatalf("a refused render returned a body: %q", body)
			}
		})
	}
}

func TestPathsAreUnderTheStateVolume(t *testing.T) {
	for _, tc := range []struct {
		got  string
		want string
	}{
		{Dir(stateDir), "/var/lib/socialweb/openbao"},
		{StorageDir(stateDir), "/var/lib/socialweb/openbao/root"},
		{ConfigPath(stateDir), "/var/lib/socialweb/openbao/root.hcl"},
		{TLSDir(stateDir), "/var/lib/socialweb/openbao/tls"},
		{TLSCAPath(stateDir), "/var/lib/socialweb/openbao/tls/ca.crt"},
		{ServerCertPath(stateDir), "/var/lib/socialweb/openbao/tls/server.crt"},
		{ServerKeyPath(stateDir), "/var/lib/socialweb/openbao/tls/server.key"},
		{ClientCertPath(stateDir), "/var/lib/socialweb/openbao/tls/client.crt"},
		{ClientKeyPath(stateDir), "/var/lib/socialweb/openbao/tls/client.key"},
	} {
		if tc.got != tc.want {
			t.Fatalf("path is %q, want %q", tc.got, tc.want)
		}
		if !strings.HasPrefix(tc.got, stateDir+string(filepath.Separator)) {
			t.Fatalf("%q is not under the state dir %q", tc.got, stateDir)
		}
	}
}
