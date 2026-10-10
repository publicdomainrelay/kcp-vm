package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/eso"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

func tenantAddOns(cfg Config, networks *subnetallocator.Allocator, log *slog.Logger) func(context.Context, string, *rest.Config) error {
	return func(ctx context.Context, cluster string, restCfg *rest.Config) error {
		if cfg.ESOManifestDir == "" {
			return fmt.Errorf("clusterprovider: no ESO manifest directory is configured, so the "+
				"OpenBao-backed tenant store for %s has no manifests to apply", cluster)
		}
		dataDir, err := clusterprovider.ClusterDataDir(cfg.StateDir, cluster)
		if err != nil {
			return err
		}
		seal, err := clusterprovider.ReadVaultSeal(dataDir)
		if err != nil {
			return fmt.Errorf("clusterprovider: reading the cluster vault's unseal material for the "+
				"tenant add-ons of %s: %w", cluster, err)
		}
		lease, err := networks.Allocate(subnetallocator.Request{Cluster: cluster})
		if err != nil {
			return fmt.Errorf("clusterprovider: the gateway the ESO store for %s dials the vault on: %w",
				cluster, err)
		}
		gateway := lease.Gateway().String()
		vaultTLS, err := vaultcert.EnsureVaultTLSCertificate(dataDir, gateway, "127.0.0.1")
		if err != nil {
			return err
		}
		vaultURL := "https://" + net.JoinHostPort(gateway, strconv.Itoa(seal.Port))
		if err := vaultcert.WriteESODemoSecret(ctx, clusterprovider.VaultAddr(seal.Port),
			seal.RootToken, map[string]any{"demo": "openbao:" + cluster}, vaultTLS.CAPEM); err != nil {
			return fmt.Errorf("clusterprovider: writing the ESO demo value the tenant's "+
				"ExternalSecret reads out of the cluster vault for %s: %w", cluster, err)
		}
		cert, err := vaultcert.MintESOCertificate(ctx, clusterprovider.VaultAddr(seal.Port),
			seal.RootToken, vaultlayout.RoleESO, vaultURL, vaultTLS.CAPEM)
		if err != nil {
			return fmt.Errorf("clusterprovider: minting the ESO certificate for %s: %w", cluster, err)
		}
		dyn, err := dynamic.NewForConfig(restCfg)
		if err != nil {
			return fmt.Errorf("clusterprovider: a dynamic client for %s's tenant add-ons: %w", cluster, err)
		}
		install := func(ctx context.Context) error {
			log.Info("cluster provider: installing the External Secrets Operator into the tenant cluster",
				"cluster", cluster, "manifests", cfg.ESOManifestDir)
			return installExternalSecrets(ctx, restCfg, cfg.ESOManifestDir)
		}
		if err := applyTenantAddOns(ctx, dyn, install, eso.Options{
			ManifestDir:   cfg.ESOManifestDir,
			Namespace:     kcpclient.TenantNamespace,
			VaultServer:   vaultURL,
			ServerCAPEM:   vaultTLS.CAPEM,
			ClientCertPEM: cert.CertPEM,
			ClientKeyPEM:  cert.KeyPEM,
		}); err != nil {
			return err
		}
		log.Info("cluster provider: applied the tenant add-ons",
			"cluster", cluster, "manifests", cfg.ESOManifestDir,
			"namespace", kcpclient.TenantNamespace, "vault", vaultURL,
			"note", "a cluster with zero nodes accepts the objects; the ESO controller pod stays "+
				"Pending because it has no node to schedule on")
		return nil
	}
}

func applyTenantAddOns(ctx context.Context, dyn dynamic.Interface, install func(context.Context) error, o eso.Options) error {
	if err := eso.InstallOperator(ctx, dyn, install); err != nil {
		return err
	}
	return eso.Apply(ctx, dyn, o)
}

func installExternalSecrets(ctx context.Context, restCfg *rest.Config, manifestDir string) error {
	script := filepath.Join(manifestDir, "install.sh")
	dir, err := os.MkdirTemp("", "socialweb-eso-install")
	if err != nil {
		return fmt.Errorf("clusterprovider: a directory for the External Secrets installer's kubeconfig: %w", err)
	}
	defer os.RemoveAll(dir)
	doc, err := clusterprovider.Kubeconfig("tenant", restCfg)
	if err != nil {
		return err
	}
	kubeconfig := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kubeconfig, doc, 0o600); err != nil {
		return fmt.Errorf("clusterprovider: writing the External Secrets installer's kubeconfig: %w", err)
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "KUBECONFIG=") {
			continue
		}
		env = append(env, kv)
	}
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Env = append(env, "KUBECONFIG="+kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clusterprovider: the External Secrets install %s failed: %w: %s",
			script, err, strings.TrimSpace(string(out)))
	}
	return nil
}
