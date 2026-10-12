package vaultcert

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/rootvault"
)

const (
	OpenBaoClientsCACommonName    = "socialweb-openbao-clients-ca"
	OpenBaoClientCommonNamePrefix = "socialweb-openbao-client-"
	openBaoClientDirMode          = 0o700
	openBaoClientMaterialFileMode = 0o600
)

type OpenBaoClientsCA struct {
	CAPEM      []byte
	BundlePath string
	key        *ecdsa.PrivateKey
	cert       *x509.Certificate
}

type OpenBaoClientTLS struct {
	CommonName string
	CertFile   string
	KeyFile    string
}

func OpenBaoClientCommonName(name string) string { return OpenBaoClientCommonNamePrefix + name }

func EnsureOpenBaoClientsCA(stateDir string, rootCAPEM []byte) (OpenBaoClientsCA, error) {
	if stateDir == "" {
		return OpenBaoClientsCA{}, ErrNoRootVaultTLSStateDir
	}
	caPath, keyPath := rootvault.ClientsCAPath(stateDir), rootvault.ClientsCAKeyPath(stateDir)
	ca := OpenBaoClientsCA{BundlePath: rootvault.ClientCABundlePath(stateDir)}
	caPEM, cert, ok := vaultTLSCAOnDisk(VaultTLS{CAPath: caPath})
	var key *ecdsa.PrivateKey
	if ok {
		key, ok = vaultTLSCAKeyOnDisk(cert, VaultTLS{CAKeyPath: keyPath})
	}
	if !ok {
		var der []byte
		var err error
		key, cert, der, err = newNamedCA(time.Now(), OpenBaoClientsCACommonName)
		if err != nil {
			return OpenBaoClientsCA{}, err
		}
		caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return OpenBaoClientsCA{}, fmt.Errorf("vaultcert: marshalling the OpenBao clients CA key: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(caPath), openBaoClientDirMode); err != nil {
			return OpenBaoClientsCA{}, err
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), openBaoClientMaterialFileMode); err != nil {
			return OpenBaoClientsCA{}, fmt.Errorf("vaultcert: writing %s: %w", keyPath, err)
		}
		if err := os.WriteFile(caPath, caPEM, openBaoClientMaterialFileMode); err != nil {
			return OpenBaoClientsCA{}, fmt.Errorf("vaultcert: writing %s: %w", caPath, err)
		}
	}
	bundle := append(append(append([]byte{}, bytes.TrimSpace(rootCAPEM)...), '\n'), caPEM...)
	if cur, err := os.ReadFile(ca.BundlePath); err != nil || !bytes.Equal(cur, bundle) {
		if err := os.WriteFile(ca.BundlePath, bundle, openBaoClientMaterialFileMode); err != nil {
			return OpenBaoClientsCA{}, fmt.Errorf("vaultcert: writing %s: %w", ca.BundlePath, err)
		}
	}
	ca.CAPEM, ca.key, ca.cert = caPEM, key, cert
	return ca, nil
}

func (ca OpenBaoClientsCA) EnsureClient(stateDir, name string) (OpenBaoClientTLS, error) {
	out := OpenBaoClientTLS{CommonName: OpenBaoClientCommonName(name), CertFile: rootvault.AppClientCertPath(stateDir, name),
		KeyFile: rootvault.AppClientKeyPath(stateDir, name)}
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	if leaf, ok := usableLeaf(out.CertFile, out.KeyFile, pool, x509.ExtKeyUsageClientAuth); ok && leaf.Subject.CommonName == out.CommonName {
		return out, nil
	}
	certPEM, keyPEM, err := signLeaf(time.Now(), ca.key, ca.cert, out.CommonName, x509.ExtKeyUsageClientAuth, nil, nil)
	if err != nil {
		return OpenBaoClientTLS{}, err
	}
	if err := os.MkdirAll(filepath.Dir(out.CertFile), openBaoClientDirMode); err != nil {
		return OpenBaoClientTLS{}, err
	}
	if err := os.WriteFile(out.KeyFile, keyPEM, openBaoClientMaterialFileMode); err != nil {
		return OpenBaoClientTLS{}, fmt.Errorf("vaultcert: writing %s: %w", out.KeyFile, err)
	}
	if err := os.WriteFile(out.CertFile, append(append([]byte{}, certPEM...), ca.CAPEM...), openBaoClientMaterialFileMode); err != nil {
		return OpenBaoClientTLS{}, fmt.Errorf("vaultcert: writing %s: %w", out.CertFile, err)
	}
	if _, err := tls.LoadX509KeyPair(out.CertFile, out.KeyFile); err != nil {
		return OpenBaoClientTLS{}, fmt.Errorf("vaultcert: the client pair just written at %s: %w", out.CertFile, err)
	}
	return out, nil
}
