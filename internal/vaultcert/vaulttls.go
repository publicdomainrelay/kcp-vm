package vaultcert

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

	CAKeyPath string

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
		CertFile:  clusterstack.OpenBaoTLSCertPath(dataDir),
		KeyFile:   clusterstack.OpenBaoTLSKeyPath(dataDir),
		CAPath:    clusterstack.OpenBaoTLSCAPath(dataDir),
		CAKeyPath: clusterstack.OpenBaoTLSCAKeyPath(dataDir),
	}
	if reuse, ok := reuseVaultTLS(out, dns, ips); ok {
		return reuse, nil
	}
	caPEM, caCert, ok := vaultTLSCAOnDisk(out)
	if !ok {
		return generateVaultTLS(out, dns, ips)
	}
	caKey, ok := vaultTLSCAKeyOnDisk(caCert, out)
	if ok {
		existingDNS, existingIPs := existingLeafHosts(out.CertFile, out.KeyFile)
		dns, ips = unionHosts(existingDNS, existingIPs, dns, ips)
		return signVaultTLS(out, time.Now(), caKey, caCert, caPEM, dns, ips)
	}
	if keep, _, ok := usableVaultTLSPair(out); ok {
		return keep, nil
	}
	return generateVaultTLS(out, dns, ips)
}

func vaultTLSCAOnDisk(v VaultTLS) ([]byte, *x509.Certificate, bool) {
	caPEM, err := os.ReadFile(v.CAPath)
	if err != nil {
		return nil, nil, false
	}
	block, _ := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return nil, nil, false
	}
	if time.Until(cert.NotAfter) < vaultTLSRenewBefore {
		return nil, nil, false
	}
	return caPEM, cert, true
}

func vaultTLSCAKeyOnDisk(caCert *x509.Certificate, v VaultTLS) (*ecdsa.PrivateKey, bool) {
	keyPEM, err := os.ReadFile(v.CAKeyPath)
	if err != nil {
		return nil, false
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, false
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, false
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, false
	}
	want, err := x509.MarshalPKIXPublicKey(caCert.PublicKey)
	if err != nil {
		return nil, false
	}
	got, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil || !bytes.Equal(want, got) {
		return nil, false
	}
	return key, true
}

func usableVaultTLSPair(v VaultTLS) (VaultTLS, *x509.Certificate, bool) {
	caPEM, err := os.ReadFile(v.CAPath)
	if err != nil {
		return VaultTLS{}, nil, false
	}
	pair, err := tls.LoadX509KeyPair(v.CertFile, v.KeyFile)
	if err != nil {
		return VaultTLS{}, nil, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return VaultTLS{}, nil, false
	}
	if time.Until(leaf.NotAfter) < vaultTLSRenewBefore {
		return VaultTLS{}, nil, false
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return VaultTLS{}, nil, false
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return VaultTLS{}, nil, false
	}
	v.CAPEM = caPEM
	return v, leaf, true
}

func existingLeafHosts(certFile, keyFile string) ([]string, []netip.Addr) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil
	}
	var ips []netip.Addr
	for _, raw := range leaf.IPAddresses {
		if addr, err := netip.ParseAddr(raw.String()); err == nil {
			ips = append(ips, addr)
		}
	}
	return leaf.DNSNames, ips
}

func unionHosts(dns []string, ips []netip.Addr, moreDNS []string, moreIPs []netip.Addr) ([]string, []netip.Addr) {
	seenDNS := make(map[string]bool, len(dns))
	for _, d := range dns {
		seenDNS[d] = true
	}
	for _, d := range moreDNS {
		if !seenDNS[d] {
			seenDNS[d] = true
			dns = append(dns, d)
		}
	}
	seenIP := make(map[netip.Addr]bool, len(ips))
	for _, ip := range ips {
		seenIP[ip.Unmap()] = true
	}
	for _, ip := range moreIPs {
		if !seenIP[ip.Unmap()] {
			seenIP[ip.Unmap()] = true
			ips = append(ips, ip)
		}
	}
	return dns, ips
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
	reuse, leaf, ok := usableVaultTLSPair(v)
	if !ok || !coversHosts(leaf, dns, ips) {
		return VaultTLS{}, false
	}
	return reuse, true
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
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return VaultTLS{}, fmt.Errorf("vaultcert: marshalling the vault TLS CA key: %w", err)
	}
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER})

	out, err := signVaultTLS(v, now, caKey, caCert, caPEM, dns, ips)
	if err != nil {
		return VaultTLS{}, err
	}
	for _, f := range []struct {
		path string
		data []byte
	}{
		{v.CAPath, caPEM},
		{v.CAKeyPath, caKeyPEM},
	} {
		if err := os.WriteFile(f.path, f.data, vaultTLSFileMode); err != nil {
			return VaultTLS{}, fmt.Errorf("vaultcert: writing %s: %w", f.path, err)
		}
	}
	return out, nil
}

func signVaultTLS(v VaultTLS, now time.Time, caKey *ecdsa.PrivateKey, caCert *x509.Certificate, caPEM []byte, dns []string, ips []netip.Addr) (VaultTLS, error) {
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
