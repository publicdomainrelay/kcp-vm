package vaultmain

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func kcpRootDir(cfg Config) string {
	if cfg.KCPRootDir != "" {
		return cfg.KCPRootDir
	}
	return filepath.Dir(cfg.KCPKubeconfig)
}

func kcpAdminTokenStorePath(cfg Config) string {
	return filepath.Join(kcpRootDir(cfg), ".admin-token-store")
}

func kcpAdminTokenStoreData(cfg Config) (map[string]any, error) {
	path := kcpAdminTokenStorePath(cfg)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the shard-admin token store kcp writes at startup is not readable at %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("the shard-admin token store at %s is empty, so kcp has not written the token hash yet", path)
	}
	return map[string]any{"token-hash": hex.EncodeToString(raw)}, nil
}
