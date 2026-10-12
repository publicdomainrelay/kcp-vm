package tlsfront

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	CertName       = "tls.crt"
	KeyName        = "tls.key"
	SelfSignedCN   = "socialweb.local"
	SelfSignedLife = 10 * 365 * 24 * time.Hour
	DialTimeout    = 10 * time.Second
	HandshakeLimit = 15 * time.Second
)

var SelfSignedNames = []string{"localhost", "socialweb.local"}

type Config struct {
	Address  string
	CertFile string
	KeyFile  string
	Backend  func() string
	OnListen func(net.Addr)
	Log      *slog.Logger
}

func (c Config) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log.With("component", "tls-front")
}

func EnsureSelfSigned(dir string) (string, string, error) {
	cert, key := filepath.Join(dir, CertName), filepath.Join(dir, KeyName)
	if st, err := os.Stat(cert); err == nil && st.Size() > 0 {
		if st, err := os.Stat(key); err == nil && st.Size() > 0 {
			return cert, key, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: SelfSignedCN},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(SelfSignedLife), DNSNames: SelfSignedNames,
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return "", "", err
	}
	kder, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	if err := writeFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return "", "", err
	}
	if err := writeFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", err
	}
	return cert, key, nil
}

func writeFile(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Fingerprint(certFile string) (string, error) {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return "", fmt.Errorf("tlsfront: %s holds no PEM certificate", certFile)
	}
	sum := sha256.Sum256(b.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

func Run(ctx context.Context, c Config) error {
	if c.Backend == nil {
		return errors.New("tlsfront: no backend")
	}
	pair, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return fmt.Errorf("tlsfront: the certificate %s and key %s: %w", c.CertFile, c.KeyFile, err)
	}
	ln, err := net.Listen("tcp", c.Address)
	if err != nil {
		return fmt.Errorf("tlsfront: listening on %s: %w", c.Address, err)
	}
	fp, _ := Fingerprint(c.CertFile)
	c.log().Info("listening", "addr", ln.Addr().String(), "cert", c.CertFile, "sha256", fp)
	if c.OnListen != nil {
		c.OnListen(ln.Addr())
	}
	tl := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	var wg sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { tl.Close() })
	defer stop()
	for {
		conn, err := tl.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("tlsfront: accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			proxy(ctx, c, conn)
		}()
	}
}

func proxy(ctx context.Context, c Config, conn net.Conn) {
	defer conn.Close()
	tc := conn.(*tls.Conn)
	hctx, cancel := context.WithTimeout(ctx, HandshakeLimit)
	err := tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		c.log().Debug("handshake", "remote", conn.RemoteAddr().String(), "err", err.Error())
		return
	}
	backend := c.Backend()
	d := net.Dialer{Timeout: DialTimeout}
	up, err := d.DialContext(ctx, "tcp", backend)
	if err != nil {
		c.log().Warn("dialing the backend", "backend", backend, "err", err.Error())
		return
	}
	defer up.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close(); up.Close() })
	defer stop()
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(up, conn)
		if cw, ok := up.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(conn, up)
		tc.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
}
