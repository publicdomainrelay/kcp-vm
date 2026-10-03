package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	envKCPRootDir   = "SOCIALWEB_KCP_ROOT"
	envClusterStore = "SOCIALWEB_CLUSTERPROVIDER_STORE_ENDPOINT"
)

func kcpRootDir(cfg Config) string {
	if root := strings.TrimSpace(os.Getenv(envKCPRootDir)); root != "" {
		return root
	}
	if cfg.KCPKubeconfig != "" {
		return filepath.Dir(cfg.KCPKubeconfig)
	}
	return filepath.Join(cfg.StateDir, "kcp")
}

func kcpAdminTokenStorePath(cfg Config) string {
	return filepath.Join(kcpRootDir(cfg), ".admin-token-store")
}

func kcpAdminTokenStoreData(cfg Config) (map[string]any, error) {
	path := kcpAdminTokenStorePath(cfg)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the shard-admin token store kcp writes at startup is not readable "+
			"at %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("the shard-admin token store at %s is empty, so kcp has not written "+
			"the token hash yet", path)
	}
	return map[string]any{"token-hash": hex.EncodeToString(raw)}, nil
}

func kcpClientCAData(cfg Config) (map[string]any, error) {
	pem, err := rootVaultPKICAPEM(cfg)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ca.crt": pem}, nil
}

func rootVaultPKICAPEM(cfg Config) (string, error) {
	material, err := readUnsealFile(cfg.UnsealPath)
	if err != nil {
		return "", fmt.Errorf("the root vault's own PKI CA, the CA kcp must be handed as "+
			"--client-ca-file, cannot be read without the unseal material at %s: %w", cfg.UnsealPath, err)
	}
	tlsConf, err := rootVaultClientTLS(cfg.StateDir)
	if err != nil {
		return "", err
	}
	client := newAPI(cfg.Addr, material.RootToken, tlsConf)
	return fetchPKICAPEM(context.Background(), client, vaultlayout.MountPKIRoot)
}

func fetchPKICAPEM(ctx context.Context, client *api, mount string) (string, error) {
	path := strings.Trim(mount, "/") + "/ca/pem"
	status, body, err := client.raw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("openbao: GET /v1/%s -> HTTP %d: %s",
			path, status, strings.TrimSpace(string(body)))
	}
	pem := strings.TrimSpace(string(body))
	if pem == "" {
		return "", fmt.Errorf("openbao: GET /v1/%s returned an empty CA, so kcp would trust no "+
			"client certificate at all", path)
	}
	return pem, nil
}

func externalStoreNamed() bool {
	return strings.TrimSpace(os.Getenv(envClusterStore)) != ""
}

func centralStoreValues(cfg Config) []string {
	return []string{
		cfg.CentralStoreEndpoint,
		cfg.CentralStoreBucket,
		cfg.CentralStoreAccessKey,
		cfg.CentralStoreSecretKey,
	}
}
