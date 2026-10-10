package cloudprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const snapshotMode = 0o600

var ErrNoSnapshot = errors.New("cloudprovider: the instance snapshot does not exist")

var ErrNoSnapshotPath = errors.New(
	"cloudprovider: the instance snapshot needs a path. Without one the provider would write an " +
		"instance list no cloud controller manager can read, and every Node would read as unknown")

func WriteSnapshot(path string, instances []Instance) error {
	if path == "" {
		return ErrNoSnapshotPath
	}
	if instances == nil {
		instances = []Instance{}
	}
	body, err := json.Marshal(instances)
	if err != nil {
		return fmt.Errorf("cloudprovider: rendering the instance snapshot for %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cloudprovider: the directory %s holding the instance snapshot: %w", dir, err)
	}
	if current, err := os.ReadFile(path); err == nil && string(current) == string(body) {
		return nil
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".")
	if err != nil {
		return fmt.Errorf("cloudprovider: a temporary file beside %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(snapshotMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cloudprovider: the mode of %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cloudprovider: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cloudprovider: closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cloudprovider: renaming %s over %s: %w", tmp.Name(), path, err)
	}
	return nil
}
