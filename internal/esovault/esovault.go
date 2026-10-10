package esovault

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"k8s.io/client-go/dynamic"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/eso"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

type VaultStore struct {
	Cluster string

	DataDir string

	VaultPort int

	RootToken string

	ManifestDir string

	Namespace string
}

func StoreVaultURL(vaultPort int) string {
	return "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(vaultPort))
}

func ApplyVaultStore(ctx context.Context, dyn dynamic.Interface, o VaultStore) (string, error) {
	if o.DataDir == "" || o.VaultPort == 0 || o.RootToken == "" || o.Namespace == "" {
		return "", errors.New("esovault: the vault-backed store needs the cluster's data dir, its vault's " +
			"leased port and root token, and the tenant namespace the ExternalSecret is written into")
	}
	if err := eso.InstallOperator(ctx, dyn, nil); err != nil {
		return "", fmt.Errorf("%w. The External Secrets controller runs beside the control plane and "+
			"its process-group step installs the CRDs before it starts, so a tenant that serves none "+
			"of them is one whose group came up without that step", err)
	}
	vaultURL := StoreVaultURL(o.VaultPort)
	vaultTLS, err := vaultcert.EnsureServedVaultTLS(ctx, o.DataDir, o.VaultPort, "127.0.0.1")
	if err != nil {
		return "", fmt.Errorf("esovault: the store for %s would be told to dial %s, and the vault that is "+
			"running cannot present that host: %w", o.Cluster, vaultURL, err)
	}
	if err := vaultcert.WriteESODemoSecret(ctx, vaultURL, o.RootToken,
		map[string]any{"demo": "openbao:" + o.Cluster}, vaultTLS.CAPEM); err != nil {
		return "", fmt.Errorf("esovault: writing the demo value the tenant's ExternalSecret reads out of "+
			"the cluster vault for %s: %w", o.Cluster, err)
	}
	cert, err := vaultcert.MintESOCertificate(ctx, vaultURL, o.RootToken,
		vaultlayout.RoleESO, vaultURL, vaultTLS.CAPEM)
	if err != nil {
		return "", fmt.Errorf("esovault: minting the store's client certificate for %s: %w", o.Cluster, err)
	}
	if err := eso.Apply(ctx, dyn, eso.Options{
		ManifestDir:   o.ManifestDir,
		Namespace:     o.Namespace,
		VaultServer:   vaultURL,
		ServerCAPEM:   vaultTLS.CAPEM,
		ClientCertPEM: cert.CertPEM,
		ClientKeyPEM:  cert.KeyPEM,
	}); err != nil {
		return "", err
	}
	return vaultURL, nil
}
