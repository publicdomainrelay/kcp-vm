package nodefirecracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const KernelDiscover = "discover"

const kernelModulesRoot = "/lib/modules"

const kernelReleaseFile = "/proc/sys/kernel/osrelease"

const kernelSourceFile = "vmlinuz"

const kernelConfigFile = "config"

type KernelOrigin string

const (
	KernelNamed           KernelOrigin = "named"
	KernelHostRunning     KernelOrigin = "host-running"
	KernelNewestInstalled KernelOrigin = "newest-installed"
)

type KernelResolution struct {
	Source string

	Config string

	Modules string

	Version string

	Origin KernelOrigin
}

type KernelResolver struct {
	ModulesRoot string

	Release func() (string, error)
}

func HostKernelResolver() KernelResolver {
	return KernelResolver{ModulesRoot: kernelModulesRoot, Release: hostKernelRelease}
}

func hostKernelRelease() (string, error) {
	raw, err := os.ReadFile(kernelReleaseFile)
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: reading %s, which names the kernel this host is "+
			"running and is what %q resolves the kernel, its configuration and its module tree from: "+
			"%w", kernelReleaseFile, KernelDiscover, err)
	}
	release := strings.TrimSpace(string(raw))
	if release == "" {
		return "", fmt.Errorf("nodefirecracker: %s is empty, so the kernel this host is running "+
			"cannot be named and %q has nothing to resolve", kernelReleaseFile, KernelDiscover)
	}
	return release, nil
}

func (r KernelResolver) Resolve(source, config, modules string) (KernelResolution, error) {
	discovering := 0
	for _, value := range []string{source, config, modules} {
		if value == KernelDiscover {
			discovering++
		}
	}
	if discovering == 0 {
		return KernelResolution{Source: source, Config: config, Modules: modules, Origin: KernelNamed}, nil
	}
	if discovering != 3 {
		return KernelResolution{}, fmt.Errorf("nodefirecracker: kernel_source, kernel_config and "+
			"module_source describe one kernel, and one of them is %q while the others name paths. A "+
			"guest boots one kernel and loads the module tree built beside it, so a mixture here "+
			"pairs whatever %q resolves with a tree named by hand and nothing compares the two",
			KernelDiscover, KernelDiscover)
	}
	resolution, runningErr := r.atRunning()
	if runningErr == nil {
		return resolution, nil
	}
	newest, newestErr := r.newest()
	if newestErr != nil {
		return KernelResolution{}, fmt.Errorf("nodefirecracker: %q asks for the kernel this host is "+
			"running, and no installed kernel under %s can be resolved either. The running kernel "+
			"could not be used (%v), and no installed kernel carries both a %s and a %s (%v)",
			KernelDiscover, r.ModulesRoot, runningErr, kernelSourceFile, kernelConfigFile, newestErr)
	}
	return newest, nil
}

func (r KernelResolver) atRunning() (KernelResolution, error) {
	release, err := r.Release()
	if err != nil {
		return KernelResolution{}, err
	}
	return r.at(release, KernelHostRunning)
}

func (r KernelResolver) at(release string, origin KernelOrigin) (KernelResolution, error) {
	dir := filepath.Join(r.ModulesRoot, release)
	source := filepath.Join(dir, kernelSourceFile)
	config := filepath.Join(dir, kernelConfigFile)
	if err := kernelFilePresent(source); err != nil {
		return KernelResolution{}, err
	}
	if err := kernelFilePresent(config); err != nil {
		return KernelResolution{}, err
	}
	return canonicalKernel(KernelResolution{
		Source:  source,
		Config:  config,
		Modules: dir,
		Version: release,
		Origin:  origin,
	})
}

func (r KernelResolver) newest() (KernelResolution, error) {
	entries, err := os.ReadDir(r.ModulesRoot)
	if err != nil {
		return KernelResolution{}, err
	}
	best := ""
	var bestAt time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		release := entry.Name()
		dir := filepath.Join(r.ModulesRoot, release)
		info, err := os.Stat(filepath.Join(dir, kernelSourceFile))
		if err != nil {
			continue
		}
		if err := kernelFilePresent(filepath.Join(dir, kernelConfigFile)); err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestAt) ||
			(info.ModTime().Equal(bestAt) && release > best) {
			best = release
			bestAt = info.ModTime()
		}
	}
	if best == "" {
		return KernelResolution{}, fmt.Errorf("no directory carries both a %s and a %s",
			kernelSourceFile, kernelConfigFile)
	}
	return r.at(best, KernelNewestInstalled)
}

func kernelFilePresent(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}

func canonicalKernel(resolution KernelResolution) (KernelResolution, error) {
	for _, path := range []*string{&resolution.Source, &resolution.Config, &resolution.Modules} {
		resolved, err := resolvePath(*path)
		if err != nil {
			return KernelResolution{}, err
		}
		*path = resolved
	}
	return resolution, nil
}
