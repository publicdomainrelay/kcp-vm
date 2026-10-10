//go:build linux

package vaultcert

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func serveStaticLeaf(t *testing.T, v VaultTLS) int {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(v.CertFile, v.KeyFile)
	if err != nil {
		t.Fatalf("loading the leaf at %s: %v", v.CertFile, err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_ = c.(*tls.Conn).Handshake()
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestAReloadThatDoesNotTakeKeepsTheRefusal(t *testing.T) {
	dir := t.TempDir()
	started, err := EnsureVaultTLSCertificate(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
	}
	port := serveStaticLeaf(t, started)
	var reloads atomic.Int32
	_, err = ServedVaultTLS{
		DataDir: dir,
		Port:    port,
		Hosts:   []string{"10.100.0.1", "127.0.0.1"},
		Reload: func(context.Context, string, int) error {
			reloads.Add(1)
			return nil
		},
		Settle: 300 * time.Millisecond,
	}.Ensure(context.Background())
	if !errors.Is(err, ErrVaultReloadDidNotTake) {
		t.Fatalf("a listener that kept its old leaf after the reload = %v, want ErrVaultReloadDidNotTake: "+
			"a reload whose completion is not observed would let one that did not happen pass", err)
	}
	if !errors.Is(err, ErrVaultServesAnotherLeaf) {
		t.Fatalf("the refusal = %v, want it to carry the served-leaf reading", err)
	}
	if got := reloads.Load(); got != 1 {
		t.Fatalf("the reload was asked for %d times, want 1", got)
	}
}

func TestAServedLeafThatCoversTheHostsAsksForNoReload(t *testing.T) {
	dir := t.TempDir()
	started, err := EnsureVaultTLSCertificate(dir, "10.100.0.1", "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
	}
	port := serveStaticLeaf(t, started)
	var reloads atomic.Int32
	if _, err := (ServedVaultTLS{
		DataDir: dir,
		Port:    port,
		Hosts:   []string{"127.0.0.1"},
		Reload: func(context.Context, string, int) error {
			reloads.Add(1)
			return nil
		},
	}).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure over a listener that already serves the host: %v", err)
	}
	if got := reloads.Load(); got != 0 {
		t.Fatalf("the reload was asked for %d times over a leaf that already covers the host, want 0", got)
	}
}

func TestAListenerThatIsNotTheVaultIsNeverSignalled(t *testing.T) {
	dir := t.TempDir()
	started, err := EnsureVaultTLSCertificate(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("EnsureVaultTLSCertificate: %v", err)
	}
	port := serveStaticLeaf(t, started)
	_, err = EnsureServedVaultTLS(context.Background(), dir, port, "10.100.0.1", "127.0.0.1")
	if !errors.Is(err, ErrNoVaultToReload) {
		t.Fatalf("the vault's port held by this test binary = %v, want ErrNoVaultToReload: a process "+
			"whose command line is not this cluster's vault must never be signalled", err)
	}
	if !errors.Is(err, ErrVaultServesAnotherLeaf) {
		t.Fatalf("the refusal = %v, want it to carry the served-leaf reading", err)
	}
}
