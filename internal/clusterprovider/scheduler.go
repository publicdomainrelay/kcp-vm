package clusterprovider

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
)

const kubeSchedulerConfigMode = 0o600

var ErrKubeSchedulerConfigUnwritable = errors.New(
	"clusterprovider: the kube-scheduler config could not be written into the cluster's data " +
		"dir. k3s hands this path to kube-scheduler as it stands, and a scheduler that cannot " +
		"read it does not come up at all -- the market cluster still serves its apiserver, so " +
		"what an operator sees is a cluster whose pods are never placed, not a cluster that " +
		"failed to start")

func WriteKubeSchedulerConfig(spec clusterstack.Spec) error {
	if !spec.MarketDialled {
		return nil
	}
	body, err := spec.KubeSchedulerConfig()
	if err != nil {
		return err
	}
	path := spec.KubeSchedulerConfigFile()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: the directory %s holding %s: %v", ErrKubeSchedulerConfigUnwritable,
			dir, path, err)
	}

	if info, err := os.Stat(path); err == nil {
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%w: reading %s: %v", ErrKubeSchedulerConfigUnwritable, path, err)
		}
		if string(current) == string(body) && info.Mode().Perm() == kubeSchedulerConfigMode {
			return nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: stat %s: %v", ErrKubeSchedulerConfigUnwritable, path, err)
	}

	tmp, err := os.CreateTemp(dir, clusterstack.KubeSchedulerConfigFileName+".")
	if err != nil {
		return fmt.Errorf("%w: a temporary file beside %s: %v", ErrKubeSchedulerConfigUnwritable,
			path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(kubeSchedulerConfigMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: the mode of %s: %v", ErrKubeSchedulerConfigUnwritable, tmp.Name(), err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: writing %s: %v", ErrKubeSchedulerConfigUnwritable, tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: closing %s: %v", ErrKubeSchedulerConfigUnwritable, tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("%w: renaming %s over %s: %v", ErrKubeSchedulerConfigUnwritable, tmp.Name(),
			path, err)
	}
	return nil
}
