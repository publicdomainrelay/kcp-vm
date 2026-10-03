package vaultcert

import (
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
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
)

const (
	vaultTLSCACommonName     = "socialweb-vault-tls-ca"
	vaultTLSServerCommonName = "socialweb-vault"
	vaultTLSValidity         = 2 * 365 * 24 * time.Hour
	vaultTLSRenewBefore      = 30 * 24 * time.Hour
	vaultTLSFileMode         = 0o600
)

type VaultTLS struct {
	CertFile string

	KeyFile string

	CAPath string

	CAPEM []byte
}

var ErrNoVaultTLSHost = errors.New(
	"vaultcert: the vault's TLS certificate names no host, so a client has no name to verify and " +
		"the listener cannot be dialed with server verification on. The vault's tenant-facing " +
		"listener is the cluster's gateway address, which is the one address a guest can dial")

func EnsureVaultTLSCertificate(dataDir string, hosts ...string) (VaultTLS, error) {
	if len(hosts) == 0 {
		return VaultTLS{}, fmt.Errorf("%w (none were given)", ErrNoVaultTLSHost)
	}
	dns, ips, err := splitHosts(hosts)
	if err != nil {
		return VaultTLS{}, err
	}
	out := VaultTLS{
		CertFile: clusterstack.OpenBaoTLSCertPath(dataDir),
		KeyFile:  clusterstack.OpenBaoTLSKeyPath(dataDir),
		CAPath:   clusterstack.OpenBaoTLSCAPath(dataDir),
	}
	if reuse, ok := reuseVaultTLS(out, dns, ips); ok {
		return reuse, nil
	}
	return generateVaultTLS(out, dns, ips)
}

func splitHosts(hosts []string) ([]string, []netip.Addr, error) {
	var dns []string
	var ips []netip.Addr
	for _, h := range hosts {
		if h == "" {
			return nil, nil, fmt.Errorf("%w (one of them is empty)", ErrNoVaultTLSHost)
		}
		if addr, err := netip.ParseAddr(h); err == nil {
			ips = append(ips, addr)
			continue
		}
		dns = append(dns, h)
	}
	return dns, ips, nil
}

func reuseVaultTLS(v VaultTLS, dns []string, ips []netip.Addr) (VaultTLS, bool) {
	caPEM, err := os.ReadFile(v.CAPath)
	if err != nil {
		return VaultTLS{}, false
	}
	pair, err := tls.LoadX509KeyPair(v.CertFile, v.KeyFile)
	if err != nil {
		return VaultTLS{}, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return VaultTLS{}, false
	}
	if time.Until(leaf.NotAfter) < vaultTLSRenewBefore {
		return VaultTLS{}, false
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return VaultTLS{}, false
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return VaultTLS{}, false
	}
	if !coversHosts(leaf, dns, ips) {
		return VaultTLS{}, false
	}
	v.CAPEM = caPEM
	return v, true
}

func coversHosts(leaf *x509.Certificate, dns []string, ips []netip.Addr) bool {
	haveDNS := map[string]bool{}
	for _, d := range leaf.DNSNames {
		haveDNS[d] = true
	}
	haveIP := map[netip.Addr]bool{}
	for _, raw := range leaf.IPAddresses {
		if addr, err := netip.ParseAddr(raw.String()); err == nil {
			haveIP[addr.Unmap()] = true
		}
	}
	for _, d := range dns {
		if !haveDNS[d] {
			return false
		}
	}
	for _, ip := range ips {
		if !haveIP[ip.Unmap()] {
			return false
		}
	}
	return true
}

func generateVaultTLS(v VaultTLS, dns []string, ips []netip.Addr) (VaultTLS, error) {
	now := time.Now()
	caKey, caCert, caDER, err := newVaultTLSCA(now)
	if err != nil {
		return VaultTLS{}, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	serverPEM, keyPEM, err := signLeaf(now, caKey, caCert, vaultTLSServerCommonName,
		x509.ExtKeyUsageServerAuth, dns, ips)
	if err != nil {
		return VaultTLS{}, err
	}
	chainPEM := append(append([]byte{}, serverPEM...), caPEM...)

	dir := filepath.Dir(v.CertFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return VaultTLS{}, fmt.Errorf("vaultcert: creating the vault's TLS directory %s: %w", dir, err)
	}
	for _, f := range []struct {
		path string
		data []byte
	}{
		{v.CAPath, caPEM},
		{v.CertFile, chainPEM},
		{v.KeyFile, keyPEM},
	} {
		if err := os.WriteFile(f.path, f.data, vaultTLSFileMode); err != nil {
			return VaultTLS{}, fmt.Errorf("vaultcert: writing %s: %w", f.path, err)
		}
	}
	v.CAPEM = caPEM
	return v, nil
}

func signLeaf(now time.Time, caKey *ecdsa.PrivateKey, caCert *x509.Certificate, commonName string, eku x509.ExtKeyUsage, dns []string, ips []netip.Addr) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("vaultcert: generating the TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("vaultcert: drawing the TLS serial: %w", err)
	}
	serial.Add(serial, big.NewInt(1))
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(vaultTLSValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{eku},
		BasicConstraintsValid: true,
		DNSNames:              dns,
	}
	for _, ip := range ips {
		template.IPAddresses = append(template.IPAddresses, ip.AsSlice())
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, key.Public(), caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("vaultcert: signing the TLS certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("vaultcert: marshalling the TLS key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func newVaultTLSCA(now time.Time) (*ecdsa.PrivateKey, *x509.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("vaultcert: generating the vault TLS CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("vaultcert: drawing the vault TLS CA serial: %w", err)
	}
	serial.Add(serial, big.NewInt(1))
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: vaultTLSCACommonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(vaultTLSValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("vaultcert: signing the vault TLS CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("vaultcert: parsing the vault TLS CA: %w", err)
	}
	return key, cert, der, nil
}
