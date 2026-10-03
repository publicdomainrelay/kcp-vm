package clusterstack

import (
	"errors"
	"strings"
	"testing"
)

func TestOpenBaoConfigIsOneLoopbackListenerWithTLS(t *testing.T) {
	s := openBaoSpec()
	body, err := s.OpenBaoServerConfig()
	if err != nil {
		t.Fatalf("OpenBaoServerConfig: %v", err)
	}
	if got := strings.Count(body, `listener "tcp"`); got != 1 {
		t.Fatalf("the config names %d listeners, want exactly 1: the vault listens on loopback "+
			"only, with TLS, so a guest's pasta forward and the admin's own call reach the same "+
			"listener and no listener binds an address that needs root:\n%s", got, body)
	}
	address := "127.0.0.1:20004"
	if !strings.Contains(body, address) {
		t.Fatalf("the single listener is not on %s:\n%s", address, body)
	}
	for _, want := range []string{
		`tls_cert_file = "` + OpenBaoTLSCertPath(s.DataDir) + `"`,
		`tls_key_file  = "` + OpenBaoTLSKeyPath(s.DataDir) + `"`,
		`api_addr = "https://` + address + `"`,
		`storage "postgresql" {`,
		openBaoDSN(),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the config does not carry %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "tls_disable") {
		t.Fatalf("the config names tls_disable, which is the plaintext listener's setting:\n%s", body)
	}
}

func TestOpenBaoConfigRefusesAListenerWithNoCertificate(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(s *Spec)
	}{
		{"no server certificate", func(s *Spec) { s.OpenBaoTLSCertFile = "" }},
		{"no server key", func(s *Spec) { s.OpenBaoTLSKeyFile = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openBaoSpec()
			tc.mut(&s)
			body, err := s.OpenBaoServerConfig()
			if !errors.Is(err, ErrNoOpenBaoTLSCertificate) {
				t.Fatalf("the config rendered for %s: err=%v body=%q", tc.name, err, body)
			}
			if body != "" {
				t.Fatalf("a refused render returned a body: %q", body)
			}
		})
	}
}
