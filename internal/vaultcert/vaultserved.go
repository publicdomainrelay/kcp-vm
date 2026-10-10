package vaultcert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const servedVaultTLSDialTimeout = 5 * time.Second

var ErrVaultServesAnotherLeaf = errors.New("vaultcert: the running vault does not present every host asked for")

func RequireServedVaultTLS(ctx context.Context, hostPort string, hosts ...string) error {
	if len(hosts) == 0 {
		return fmt.Errorf("%w (none were given)", ErrNoVaultTLSHost)
	}
	dns, ips, err := splitHosts(hosts)
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: servedVaultTLSDialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return fmt.Errorf("vaultcert: dialing the vault at %s to read the certificate it is serving: %w", hostPort, err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("vaultcert: the TLS handshake with the vault at %s, to read the certificate it is serving: %w", hostPort, err)
	}
	served := conn.ConnectionState().PeerCertificates
	if len(served) == 0 {
		return fmt.Errorf("vaultcert: the vault at %s completed a TLS handshake with no certificate, so there is nothing to check a host against and a client told to dial it would verify nothing", hostPort)
	}
	missing := hostsTheLeafDoesNotCarry(served[0], dns, ips)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the vault listening at %s is serving a certificate that does not name %v, so a client told to dial it verifies nothing and the handshake fails with a name mismatch. The leaf on disk was re-signed to carry that host, and OpenBao reads tls_cert_file at start and again only on SIGHUP, so the leaf on disk carries %v while the leaf in flight carries only the hosts the vault was last loaded with. Refusing to hand a client a URL the running listener cannot present", ErrVaultServesAnotherLeaf, hostPort, missing, missing)
}

func hostsTheLeafDoesNotCarry(leaf *x509.Certificate, dns []string, ips []netip.Addr) []string {
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
	var missing []string
	for _, d := range dns {
		if !haveDNS[d] {
			missing = append(missing, d)
		}
	}
	for _, ip := range ips {
		if !haveIP[ip.Unmap()] {
			missing = append(missing, ip.String())
		}
	}
	return missing
}
