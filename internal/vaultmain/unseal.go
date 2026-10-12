package vaultmain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type unsealFile struct {
	Threshold  int      `json:"threshold"`
	Keys       []string `json:"keys"`
	KeysBase64 []string `json:"keys_base64"`
	RootToken  string   `json:"root_token"`
}

func writeUnsealFile(path string, f unsealFile) error {
	encoded, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("socialweb-vault: encoding the unseal material: %w", err)
	}
	encoded = append(encoded, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("socialweb-vault: creating the unseal directory %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return fmt.Errorf("socialweb-vault: writing the unseal material to %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("socialweb-vault: mode 0600 on %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("socialweb-vault: moving the unseal material into %s: %w", path, err)
	}
	return nil
}

func readUnsealFile(path string) (unsealFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return unsealFile{}, fmt.Errorf("socialweb-vault: the root vault is initialised, so its "+
			"unseal material must already be at %s and it is not readable: %w", path, err)
	}
	var f unsealFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return unsealFile{}, fmt.Errorf("socialweb-vault: the unseal material at %s is not JSON: %w", path, err)
	}
	if f.RootToken == "" {
		return unsealFile{}, fmt.Errorf("socialweb-vault: the unseal material at %s carries no root token", path)
	}
	if f.Threshold <= 0 || len(f.Keys) < f.Threshold {
		return unsealFile{}, fmt.Errorf("socialweb-vault: the unseal material at %s carries %d keys for "+
			"a threshold of %d, so the vault cannot be unsealed from it", path, len(f.Keys), f.Threshold)
	}
	return f, nil
}
