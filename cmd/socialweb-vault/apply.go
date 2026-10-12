package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/rootvault"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	initShares    = 5
	initThreshold = 3

	pkiRootCommonName = "socialweb.local"

	healthWait = 90 * time.Second
)

func waitHealth(ctx context.Context, client *openbao.Client, addr string) (openbao.Health, error) {
	deadline := time.Now().Add(healthWait)
	for {
		h, err := client.Health(ctx)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, openbao.ErrTransport) && !errors.Is(err, openbao.ErrNotFound) {
			return openbao.Health{}, err
		}
		if time.Now().After(deadline) {
			return openbao.Health{}, fmt.Errorf("socialweb-vault: the root vault at %s did not answer "+
				"GET /v1/sys/health within %s: %w", addr, healthWait, err)
		}
		select {
		case <-ctx.Done():
			return openbao.Health{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func unseal(ctx context.Context, client *openbao.Client, keys []string, threshold int) error {
	for i := 0; i < threshold; i++ {
		sealed, err := client.Unseal(ctx, keys[i])
		if err != nil {
			return fmt.Errorf("socialweb-vault: unsealing with key %d of %d: %w", i+1, threshold, err)
		}
		if !sealed {
			return nil
		}
	}
	sealed, err := client.Sealed(ctx)
	if err != nil {
		return err
	}
	if sealed {
		return fmt.Errorf("socialweb-vault: the vault is still sealed after %d keys, so the "+
			"threshold in the unseal material does not match the vault's own", threshold)
	}
	return nil
}

func applyRoot(ctx context.Context, cfg Config, log *slog.Logger) error {
	tlsConf, err := rootVaultClientTLS(cfg.StateDir)
	if err != nil {
		return err
	}
	opts := []openbao.Option{
		openbao.WithCACertPool(tlsConf.RootCAs),
		openbao.WithClientCertificate(tlsConf.Certificates[0]),
	}
	client := openbao.New(cfg.Addr, "", opts...)

	health, err := waitHealth(ctx, client, cfg.Addr)
	if err != nil {
		return err
	}
	log.Info("root vault answered", "addr", cfg.Addr, "initialized", health.Initialized, "sealed", health.Sealed)

	var token string
	if !health.Initialized {
		result, err := client.Init(ctx, initShares, initThreshold)
		if err != nil {
			return fmt.Errorf("socialweb-vault: initialising the root vault: %w", err)
		}
		material := unsealFile{
			Threshold:  initThreshold,
			Keys:       result.Keys,
			KeysBase64: result.KeysBase64,
			RootToken:  result.RootToken,
		}
		if err := writeUnsealFile(cfg.UnsealPath, material); err != nil {
			return err
		}
		log.Info("initialised the root vault", "shares", initShares, "threshold", initThreshold, "unseal", cfg.UnsealPath)
		token = result.RootToken
		if err := unseal(ctx, client, result.Keys, initThreshold); err != nil {
			return err
		}
	} else {
		material, err := readUnsealFile(cfg.UnsealPath)
		if err != nil {
			return err
		}
		token = material.RootToken
		if health.Sealed {
			if err := unseal(ctx, client, material.Keys, material.Threshold); err != nil {
				return err
			}
		}
	}

	sealed, err := client.Sealed(ctx)
	if err != nil {
		return err
	}
	if sealed {
		return fmt.Errorf("socialweb-vault: the root vault is sealed, so nothing can be applied")
	}

	root := openbao.New(cfg.Addr, token, opts...)
	out := newAPI(cfg.Addr, token, tlsConf)

	layout := vaultlayout.Default()
	if err := vaultlayout.Validate(layout); err != nil {
		return fmt.Errorf("socialweb-vault: the declared layout does not validate: %w", err)
	}
	return applyLayout(ctx, cfg, root, out, layout, log)
}

func applyLayout(ctx context.Context, cfg Config, root *openbao.Client, out *api, layout vaultlayout.Layout, log *slog.Logger) error {
	if err := applyMounts(ctx, out, layout, log); err != nil {
		return err
	}
	if err := applyPolicies(ctx, root, layout); err != nil {
		return err
	}
	if err := applyPKI(ctx, root, out, layout, log); err != nil {
		return err
	}
	if err := applyAppRoles(ctx, root, out, layout, log); err != nil {
		return err
	}
	return applySecrets(ctx, cfg, root, layout, log)
}

func applyMounts(ctx context.Context, out *api, layout vaultlayout.Layout, log *slog.Logger) error {
	mounts, err := out.mounts(ctx)
	if err != nil {
		return err
	}
	for _, m := range layout.Mounts {
		if m.Vault != vaultlayout.Root {
			continue
		}
		if info, ok := mounts[m.Path]; ok {
			if !mountMatches(info, m) {
				return fmt.Errorf("socialweb-vault: %s is mounted as type %q, but the layout declares "+
					"%s, and this will not replace a mount a deployment is already using", m.Path, info.Type, m.Kind)
			}
			log.Info("mount already present", "path", m.Path, "kind", m.Kind)
			continue
		}
		if err := out.createMount(ctx, m); err != nil {
			return err
		}
		log.Info("mounted", "path", m.Path, "kind", m.Kind)
	}
	return nil
}

func applyPolicies(ctx context.Context, root *openbao.Client, layout vaultlayout.Layout) error {
	for _, p := range layout.Policies {
		if p.Vault != vaultlayout.Root {
			continue
		}
		if err := root.PutPolicy(ctx, p.Name, p.HCL); err != nil {
			return fmt.Errorf("socialweb-vault: writing policy %s: %w", p.Name, err)
		}
	}
	return nil
}

func applyPKI(ctx context.Context, root *openbao.Client, out *api, layout vaultlayout.Layout, log *slog.Logger) error {
	for _, m := range layout.Mounts {
		if m.Vault != vaultlayout.Root || m.Kind != vaultlayout.MountPKI {
			continue
		}
		present, err := out.pkiRootPresent(ctx, m.Path)
		if err != nil {
			return err
		}
		if present {
			log.Info("pki root already generated", "mount", m.Path)
			continue
		}
		ttl := m.MaxLeaseTTL
		if ttl <= 0 {
			ttl = 8760 * time.Hour
		}
		cert, err := root.GeneratePKIRoot(ctx, m.Path, pkiRootCommonName, ttl.String())
		if err != nil {
			return fmt.Errorf("socialweb-vault: generating the root CA under %s: %w", m.Path, err)
		}
		log.Info("generated the pki root", "mount", m.Path, "common_name", pkiRootCommonName, "serial", cert.Serial)
	}
	for _, r := range layout.PKIRoles {
		if r.Vault != vaultlayout.Root {
			continue
		}
		if err := out.writePKIRole(ctx, r); err != nil {
			return fmt.Errorf("socialweb-vault: writing pki role %s/%s: %w", r.Mount, r.Name, err)
		}
		log.Info("wrote pki role", "mount", r.Mount, "name", r.Name)
	}
	return nil
}

func applyAppRoles(ctx context.Context, root *openbao.Client, out *api, layout vaultlayout.Layout, log *slog.Logger) error {
	auths, err := out.auths(ctx)
	if err != nil {
		return err
	}
	if _, ok := auths["approle/"]; ok {
		log.Info("approle auth already enabled")
	} else {
		if err := out.enableAppRole(ctx); err != nil {
			return err
		}
		log.Info("enabled approle auth")
	}
	for _, r := range layout.AppRoles {
		if r.Vault != vaultlayout.Root {
			continue
		}
		if err := out.writeAppRole(ctx, r); err != nil {
			return fmt.Errorf("socialweb-vault: writing approle %s: %w", r.Name, err)
		}
		log.Info("wrote approle", "name", r.Name, "policies", r.Policies)
	}
	return nil
}

type sourcedSecret struct {
	Path          string
	Data          map[string]any
	Reason        string
	Required      bool
	NotApplicable bool
}

func planRootSecrets(layout vaultlayout.Layout, cfg Config) ([]sourcedSecret, error) {
	var out []sourcedSecret
	for _, s := range layout.Secrets {
		if s.Vault != vaultlayout.Root {
			continue
		}
		switch s.Class {
		case vaultlayout.ClassKCPAdminKubeconfig:
			raw, err := os.ReadFile(cfg.KCPKubeconfig)
			if err != nil {
				return nil, fmt.Errorf("socialweb-vault: the kcp admin kubeconfig the gateway reads "+
					"from %s: %w", cfg.KCPKubeconfig, err)
			}
			out = append(out, sourcedSecret{Path: s.Path, Data: map[string]any{"kubeconfig": string(raw)}})
		case vaultlayout.ClassKCPAdminTokenStore:
			data, err := kcpAdminTokenStoreData(cfg)
			if err != nil {
				out = append(out, sourcedSecret{Path: s.Path, Required: true, Reason: err.Error()})
				continue
			}
			out = append(out, sourcedSecret{Path: s.Path, Data: data})
		case vaultlayout.ClassKCPClientCA:
			data, err := kcpClientCAData(cfg)
			if err != nil {
				out = append(out, sourcedSecret{Path: s.Path, Required: true, Reason: err.Error()})
				continue
			}
			out = append(out, sourcedSecret{Path: s.Path, Data: data})
		case vaultlayout.ClassDeviceKeyMaterial:
			if cfg.DeviceKeyAudience == "" {
				out = append(out, sourcedSecret{Path: s.Path, Reason: fmt.Sprintf(
					"%s is not set, so the device-key audience is unknown; the entrypoint sets it from "+
						"SOCIALWEB_GATEWAY_AUDIENCES", envDeviceKeyAudience)})
				continue
			}
			data := map[string]any{"audience": cfg.DeviceKeyAudience}
			if cfg.DevkeyRegistry != "" {
				data["registry"] = cfg.DevkeyRegistry
			}
			if cfg.DeviceKeyExecEnvName != "" {
				data["exec-env-name"] = cfg.DeviceKeyExecEnvName
			}
			out = append(out, sourcedSecret{Path: s.Path, Data: data})
		case vaultlayout.ClassContainerTLS:
			if cfg.ContainerTLSCert == "" || cfg.ContainerTLSKey == "" {
				out = append(out, sourcedSecret{Path: s.Path, Reason: fmt.Sprintf(
					"%s and %s are not set, so the container TLS certificate has no source yet",
					envContainerTLSCert, envContainerTLSKey)})
				continue
			}
			cert, err := os.ReadFile(cfg.ContainerTLSCert)
			if err != nil {
				out = append(out, sourcedSecret{Path: s.Path, Reason: fmt.Sprintf(
					"the container TLS certificate %s is not readable: %v", cfg.ContainerTLSCert, err)})
				continue
			}
			key, err := os.ReadFile(cfg.ContainerTLSKey)
			if err != nil {
				out = append(out, sourcedSecret{Path: s.Path, Reason: fmt.Sprintf(
					"the container TLS key %s is not readable: %v", cfg.ContainerTLSKey, err)})
				continue
			}
			out = append(out, sourcedSecret{Path: s.Path, Data: map[string]any{
				"tls.crt": string(cert),
				"tls.key": string(key),
			}})
		default:
			out = append(out, sourcedSecret{Path: s.Path, Required: true, Reason: fmt.Sprintf(
				"class %s has no producer in this container", s.Class)})
		}
	}
	return out, nil
}

func applySecrets(ctx context.Context, cfg Config, root *openbao.Client, layout vaultlayout.Layout, log *slog.Logger) error {
	planned, err := planRootSecrets(layout, cfg)
	if err != nil {
		return err
	}
	for _, s := range planned {
		if s.Data == nil {
			switch {
			case s.NotApplicable:
				log.Info("root secret not applicable", "path", s.Path, "reason", s.Reason)
				continue
			case s.Required:
				return fmt.Errorf("socialweb-vault: the root secret at %s has no source: %s",
					s.Path, s.Reason)
			}
			log.Warn("root secret not written", "path", s.Path, "reason", s.Reason)
			continue
		}
		changed, err := putIfChanged(ctx, root, s.Path, s.Data)
		if err != nil {
			return fmt.Errorf("socialweb-vault: writing %s: %w", s.Path, err)
		}
		if changed {
			log.Info("wrote root secret", "path", s.Path, "keys", keysOf(s.Data))
		} else {
			log.Info("root secret already current", "path", s.Path)
		}
	}
	return nil
}

func putIfChanged(ctx context.Context, root *openbao.Client, path string, data map[string]any) (bool, error) {
	existing, err := root.GetKV(ctx, path)
	if err == nil && reflect.DeepEqual(existing, data) {
		return false, nil
	}
	if err != nil && !errors.Is(err, openbao.ErrNotFound) {
		return false, err
	}
	return true, root.PutKV(ctx, path, data)
}

func keysOf(data map[string]any) []string {
	out := make([]string, 0, len(data))
	for k := range data {
		out = append(out, k)
	}
	return out
}

func prepareRoot(cfg Config, log *slog.Logger) error {
	hosts := []string{rootvault.LoopbackHost}
	if host, err := addrHost(cfg.Addr); err == nil && host != "" && host != rootvault.LoopbackHost {
		hosts = append(hosts, host)
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		hosts = append(hosts, name)
	}
	material, err := vaultcert.EnsureRootVaultTLS(cfg.StateDir, hosts...)
	if err != nil {
		return err
	}
	addr, err := addrHostPort(cfg.Addr)
	if err != nil {
		return err
	}
	body, err := rootvault.ServerConfig(cfg.StateDir, addr)
	if err != nil {
		return err
	}
	configPath := rootvault.ConfigPath(cfg.StateDir)
	if err := writeFileMode(configPath, []byte(body), 0o600); err != nil {
		return err
	}
	log.Info("materialised the root vault's mutual TLS material",
		"ca", material.CAPath,
		"server_cert", material.ServerCertFile,
		"client_cert", material.ClientCertFile,
		"config", configPath,
		"sans", hosts)
	return nil
}

func rootVaultClientTLS(stateDir string) (*tls.Config, error) {
	material, err := vaultcert.LoadRootVaultTLS(stateDir)
	if err != nil {
		return nil, fmt.Errorf("socialweb-vault: the root vault's listener demands a client "+
			"certificate and trusts only its own CA, and the material the entrypoint generates is "+
			"not readable: %w", err)
	}
	return material.ClientTLSConfig()
}

func addrHost(addr string) (string, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", fmt.Errorf("socialweb-vault: %q is not a URL: %w", addr, err)
	}
	return u.Hostname(), nil
}

func addrHostPort(addr string) (string, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", fmt.Errorf("socialweb-vault: %q is not a URL the root vault can listen on: %w", addr, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("socialweb-vault: %q names no host:port for the root vault's listener", addr)
	}
	return u.Host, nil
}

func writeFileMode(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("socialweb-vault: creating %s: %w", dir, err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("socialweb-vault: writing %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("socialweb-vault: mode %04o on %s: %w", mode, path, err)
	}
	return nil
}
