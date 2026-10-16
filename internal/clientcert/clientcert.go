package clientcert

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kubeconfig"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
)

var ErrVaultUnavailable = gateway.ErrIssuerUnavailable

const (
	issueTimeout  = 10 * time.Second
	revokeTimeout = 30 * time.Second
	crlTimeout    = 5 * time.Second
	lifetimeSlack = 5 * time.Minute
)

const noExpiryNotAfter = "9999-12-31T23:59:59Z"

type Recorder interface {
	RecordCertificate(ctx context.Context, rec kcpclient.CertificateRecord) error
	MarkCertificateRevoked(ctx context.Context, did, cluster, serial string) error
}

type Issuer struct {
	StateDir string
	CAFile   string
	Log      *slog.Logger
	Recorder Recorder
}

func DataDir(stateDir, did, cluster string) (string, error) {
	key, err := kcpclient.ClusterKey(kcpclient.HomePath(did, cluster))
	if err != nil {
		return "", err
	}
	return clusterprovider.ClusterDataDir(stateDir, key)
}

func (i *Issuer) log() *slog.Logger {
	if i.Log != nil {
		return i.Log
	}
	return slog.Default()
}

func (i *Issuer) Grant(ctx context.Context, in kcpclient.GrantInput) (string, error) {
	if in.Caller == "" || in.Cluster == "" || in.Server == "" {
		return "", errors.New("clientcert: a caller, a cluster and a server are required")
	}
	ttl := in.TTL
	if ttl == 0 {
		ttl = gateway.DefaultClientCertTTL
	}
	if in.NoExpiry {
		if in.TTL != 0 {
			return "", errors.New("clientcert: a lifetime and no expiry are exclusive")
		}
	} else if ttl < gateway.MinClientCertTTL || ttl > gateway.MaxClientCertTTL {
		return "", errors.New("clientcert: the lifetime is outside the allowed window")
	}
	dir, err := DataDir(i.StateDir, string(in.Caller), in.Cluster)
	if err != nil {
		return "", err
	}
	client, _, err := clusterprovider.GatewayVaultClient(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrVaultUnavailable, err)
	}
	cctx, cancel := context.WithTimeout(ctx, issueTimeout)
	defer cancel()
	role, req := clusterprovider.VaultPKIRoleKubeconfig, openbao.CertRequest{
		CommonName:        string(in.Caller),
		TTL:               strconv.FormatInt(int64(ttl/time.Second), 10) + "s",
		ExcludeCNFromSANs: true,
	}
	if in.NoExpiry {
		role, req.TTL, req.NotAfter = clusterprovider.VaultPKIRoleKubeconfigNoExpiry, "", noExpiryNotAfter
	}
	cert, err := client.IssueCert(cctx, clusterprovider.VaultPKIPath, role, req)
	if err != nil {
		return "", fmt.Errorf("%w: issue: %v", ErrVaultUnavailable, err)
	}
	leaf, err := parseCertificate(cert.Certificate)
	if err != nil {
		return "", err
	}
	switch {
	case leaf.Subject.CommonName != string(in.Caller):
		return "", errors.New("clientcert: the issued certificate has the wrong common name")
	case !slices.Equal(leaf.Subject.Organization, []string{kcpclient.OwnersGroup}):
		return "", errors.New("clientcert: the issued certificate has the wrong organization")
	case !in.NoExpiry && leaf.NotAfter.Sub(leaf.NotBefore) > gateway.MaxClientCertTTL+lifetimeSlack:
		return "", errors.New("clientcert: the issued certificate lives too long")
	case in.NoExpiry != slices.Contains(leaf.Subject.OrganizationalUnit, clusterprovider.NoExpiryOrganizationalUnit):
		return "", errors.New("clientcert: the issued certificate has the wrong organizational unit")
	case !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth):
		return "", errors.New("clientcert: the issued certificate is not a client certificate")
	case cert.PrivateKey == "":
		return "", errors.New("clientcert: the issuer returned no private key")
	}
	if i.Recorder == nil {
		i.revokeSerial(ctx, client, cert.Serial)
		return "", fmt.Errorf("%w: no recorder for certificates", ErrVaultUnavailable)
	}
	rec := kcpclient.CertificateRecord{DID: string(in.Caller), Cluster: in.Cluster, Serial: cert.Serial,
		NotAfter: leaf.NotAfter, NoExpiry: in.NoExpiry}
	if err := i.Recorder.RecordCertificate(ctx, rec); err != nil {
		i.revokeSerial(ctx, client, cert.Serial)
		return "", fmt.Errorf("%w: record: %v", ErrVaultUnavailable, err)
	}
	var caPEM []byte
	if i.CAFile != "" {
		if caPEM, err = os.ReadFile(i.CAFile); err != nil {
			return "", fmt.Errorf("clientcert: reading the CA file: %w", err)
		}
	}
	out, err := kubeconfig.BuildClientCert(kubeconfig.ClientCertInput{ClusterName: in.Cluster, Server: in.Server,
		CACertPEM: caPEM, CertPEM: []byte(cert.Certificate), KeyPEM: []byte(cert.PrivateKey)})
	if err != nil {
		return "", err
	}
	msg := "clientcert: issued a kubeconfig certificate"
	if in.NoExpiry {
		msg = "clientcert: kubeconfig no-expiry certificate issued"
	}
	i.log().Info(msg, "did", string(in.Caller), "cluster", in.Cluster, "serial", cert.Serial,
		"notAfter", leaf.NotAfter.UTC().Format(time.RFC3339))
	return out, nil
}

func (i *Issuer) revokeSerial(ctx context.Context, client *openbao.Client, serial string) {
	if err := client.Write(ctx, "pki/revoke", map[string]any{"serial_number": serial}); err != nil {
		i.log().Warn("clientcert: the revoke of an unrecorded certificate failed", "serial", serial, "err", err.Error())
	}
}

func parseCertificate(pemText string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("clientcert: the issuer returned no certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func (i *Issuer) Revoke(ctx context.Context, did, cluster string) (int, error) {
	dir, err := DataDir(i.StateDir, did, cluster)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	client, _, err := clusterprovider.GatewayVaultClient(ctx, dir)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrVaultUnavailable, err)
	}
	serials, err := client.List(ctx, "pki/certs")
	if err != nil {
		return 0, fmt.Errorf("clientcert: listing certificates: %w", err)
	}
	count := 0
	now := time.Now()
	for _, serial := range serials {
		data, err := client.Read(ctx, "pki/cert/"+serial)
		if err != nil {
			return count, fmt.Errorf("clientcert: reading a certificate: %w", err)
		}
		text, _ := data["certificate"].(string)
		leaf, err := parseCertificate(text)
		if err != nil {
			continue
		}
		if leaf.Subject.CommonName != did || !slices.Equal(leaf.Subject.Organization, []string{kcpclient.OwnersGroup}) || !leaf.NotAfter.After(now) {
			continue
		}
		if err := client.Write(ctx, "pki/revoke", map[string]any{"serial_number": serial}); err != nil {
			return count, fmt.Errorf("clientcert: revoking a certificate: %w", err)
		}
		count++
		if i.Recorder != nil {
			if err := i.Recorder.MarkCertificateRevoked(ctx, did, cluster, serial); err != nil {
				i.log().Warn("clientcert: the record of a revoked certificate was not updated", "serial", serial, "err", err.Error())
			}
		}
	}
	i.log().Info("clientcert: revoked kubeconfig certificates", "did", did, "cluster", cluster, "count", count)
	return count, nil
}

func FetchRevoked(ctx context.Context, dataDir string) (map[string]struct{}, error) {
	ctx, cancel := context.WithTimeout(ctx, crlTimeout)
	defer cancel()
	client, _, err := clusterprovider.GatewayVaultClient(ctx, dataDir)
	if err != nil {
		return nil, err
	}
	serials, err := client.List(ctx, "pki/certs/revoked")
	if err != nil {
		return nil, fmt.Errorf("clientcert: listing revoked certificates: %w", err)
	}
	out := make(map[string]struct{}, len(serials))
	for _, s := range serials {
		out[NormalizeSerial(s)] = struct{}{}
	}
	return out, nil
}

func NormalizeSerial(s string) string {
	return kcpclient.CertSerialKey(s)
}
