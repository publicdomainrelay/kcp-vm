package openbaoctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type UnsealKeys struct {
	Threshold int      `json:"threshold"`
	Shares    int      `json:"shares"`
	Keys      []string `json:"keys_base64"`
}

var ErrNoUnsealKeys = errors.New("openbaoctl: no unseal keys")

func WriteUnsealKeys(path string, k UnsealKeys) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("openbaoctl: %s already exists; unseal keys are written once and never replaced", path)
	}
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func ReadUnsealKeys(path string) (UnsealKeys, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return UnsealKeys{}, fmt.Errorf("%w at %s", ErrNoUnsealKeys, path)
	}
	if err != nil {
		return UnsealKeys{}, err
	}
	if !fi.Mode().IsRegular() {
		return UnsealKeys{}, fmt.Errorf("openbaoctl: %s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return UnsealKeys{}, fmt.Errorf("openbaoctl: %s is mode %04o; unseal keys are read only from a file no other user can read (0600)", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return UnsealKeys{}, err
	}
	var k UnsealKeys
	if err := json.Unmarshal(b, &k); err != nil {
		return UnsealKeys{}, fmt.Errorf("openbaoctl: %s is not JSON: %w", path, err)
	}
	if k.Threshold <= 0 || len(k.Keys) < k.Threshold {
		return UnsealKeys{}, fmt.Errorf("openbaoctl: %s holds %d keys for a threshold of %d", path, len(k.Keys), k.Threshold)
	}
	return k, nil
}
