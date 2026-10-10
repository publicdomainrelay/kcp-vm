package eso

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	webhookCACommonName     = "socialweb-external-secrets-webhook-ca"
	webhookServerCommonName = "socialweb-external-secrets-webhook"
	webhookCAValidity       = 10 * 365 * 24 * time.Hour
	webhookLeafValidity     = 2 * 365 * 24 * time.Hour
	webhookTLSFileMode      = 0o600
)

type WebhookTLSPaths struct {
	CertDir string

	CAKey string
}

func (p WebhookTLSPaths) caCert() string { return filepath.Join(p.CertDir, WebhookCAName) }

func (p WebhookTLSPaths) leafCert() string { return filepath.Join(p.CertDir, WebhookCertName) }

func (p WebhookTLSPaths) leafKey() string { return filepath.Join(p.CertDir, WebhookKeyName) }

func EnsureWebhookTLS(p WebhookTLSPaths, now time.Time) ([]byte, error) {
	if p.CertDir == "" || p.CAKey == "" {
		return nil, errors.New("eso: the webhook's TLS needs the cert dir the webhook reads and a " +
			"path outside it for the CA key")
	}
	if filepath.Clean(filepath.Dir(p.CAKey)) == filepath.Clean(p.CertDir) {
		return nil, fmt.Errorf("eso: the webhook CA key %s would sit in the cert dir the webhook "+
			"process reads, which hands the process the key that signs its own trust", p.CAKey)
	}
	for _, dir := range []string{p.CertDir, filepath.Dir(p.CAKey)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("eso: creating %s for the webhook's TLS: %w", dir, err)
		}
	}
	caPEM, caCert, caKey, ok := loadWebhookCA(p, now)
	if !ok {
		var err error
		caPEM, caCert, caKey, err = newWebhookCA(now)
		if err != nil {
			return nil, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
		if err != nil {
			return nil, fmt.Errorf("eso: marshalling the webhook CA key: %w", err)
		}
		if err := writeFile(p.CAKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
			return nil, err
		}
		if err := writeFile(p.caCert(), caPEM); err != nil {
			return nil, err
		}
	}
	if leafUsable(p, caCert, now) {
		return caPEM, nil
	}
	certPEM, keyPEM, err := signWebhookLeaf(now, caKey, caCert)
	if err != nil {
		return nil, err
	}
	if err := writeFile(p.leafKey(), keyPEM); err != nil {
		return nil, err
	}
	if err := writeFile(p.leafCert(), certPEM); err != nil {
		return nil, err
	}
	return caPEM, nil
}

func loadWebhookCA(p WebhookTLSPaths, now time.Time) ([]byte, *x509.Certificate, *ecdsa.PrivateKey, bool) {
	caPEM, err := os.ReadFile(p.caCert())
	if err != nil {
		return nil, nil, nil, false
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil, nil, nil, false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || cert.NotAfter.Before(now.Add(webhookLeafValidity)) {
		return nil, nil, nil, false
	}
	keyPEM, err := os.ReadFile(p.CAKey)
	if err != nil {
		return nil, nil, nil, false
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, nil, false
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, nil, false
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || !key.PublicKey.Equal(cert.PublicKey) {
		return nil, nil, nil, false
	}
	return caPEM, cert, key, true
}

func leafUsable(p WebhookTLSPaths, ca *x509.Certificate, now time.Time) bool {
	pair, err := tls.LoadX509KeyPair(p.leafCert(), p.leafKey())
	if err != nil || len(pair.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	if leaf.NotAfter.Sub(now) < WebhookRenewBefore {
		return false
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, name := range []string{"127.0.0.1", WebhookDNSName} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, CurrentTime: now,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return false
		}
	}
	return true
}

func serial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("eso: drawing a certificate serial: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

func newWebhookCA(now time.Time) ([]byte, *x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("eso: generating the webhook CA key: %w", err)
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: webhookCACommonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(webhookCAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("eso: signing the webhook CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("eso: parsing the webhook CA: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key, nil
}

func signWebhookLeaf(now time.Time, caKey *ecdsa.PrivateKey, ca *x509.Certificate) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("eso: generating the webhook serving key: %w", err)
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, err
	}
	notAfter := now.Add(webhookLeafValidity)
	if notAfter.After(ca.NotAfter) {
		notAfter = ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: webhookServerCommonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{WebhookDNSName},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("eso: signing the webhook serving certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("eso: marshalling the webhook serving key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func writeFile(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return os.Chmod(path, webhookTLSFileMode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".")
	if err != nil {
		return fmt.Errorf("eso: a temporary file beside %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(webhookTLSFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("eso: the mode of %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("eso: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("eso: closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("eso: renaming %s over %s: %w", tmp.Name(), path, err)
	}
	return nil
}
