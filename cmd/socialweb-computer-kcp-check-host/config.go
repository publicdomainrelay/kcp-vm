package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

const (
	envConfigPath = "SOCIALWEB_CHECK_HOST_CONFIG"

	defaultConfigPath = "/home/johnandersen777/kcp-image/nodeimage.json"

	envFirecracker = "SOCIALWEB_CHECK_HOST_FIRECRACKER"

	envQEMU = "SOCIALWEB_CHECK_HOST_QEMU"

	envKernel = "SOCIALWEB_CHECK_HOST_KERNEL"

	envKernelConfig = "SOCIALWEB_CHECK_HOST_KERNEL_CONFIG"

	envModuleSource = "SOCIALWEB_CHECK_HOST_MODULE_SOURCE"

	envStageDir = "SOCIALWEB_CHECK_HOST_STAGE_DIR"

	envOutDir = "SOCIALWEB_CHECK_HOST_OUT_DIR"

	envK3s = "SOCIALWEB_CHECK_HOST_K3S"

	envKubectl = "SOCIALWEB_CHECK_HOST_KUBECTL"

	envKine = "SOCIALWEB_CHECK_HOST_KINE"

	envKCP = "SOCIALWEB_CHECK_HOST_KCP"

	envKVM = "SOCIALWEB_CHECK_HOST_KVM"

	envRetain = "SOCIALWEB_CHECK_HOST_RETAIN"

	envRepo = "SOCIALWEB_CHECK_HOST_REPO"

	envPostgresBinDir = "SOCIALWEB_CHECK_HOST_POSTGRES_BIN_DIR"

	envNodePassword = "SOCIALWEB_K3S_NODE_PASSWORD"
)

type nodeImageConfig struct {
	VMM string `json:"vmm"`

	Firecracker string `json:"firecracker"`

	QEMU string `json:"qemu"`

	KernelSource string `json:"kernel_source"`

	KernelConfig string `json:"kernel_config"`

	ModuleSource string `json:"module_source"`

	StageDir string `json:"stage_dir"`

	OutDir string `json:"out_dir"`

	K3sBinary string `json:"k3s_binary"`

	Images []string `json:"images"`

	Modules []string `json:"modules"`
}

var configKeys = []string{
	"firecracker",
	"kernel_source",
	"kernel_config",
	"module_source",
	"stage_dir",
	"out_dir",
	"k3s_binary",
	"images",
	"modules",
}

type facts struct {
	release string

	kernel string

	kernelConfig string

	moduleSource string

	stageDir string

	outDir string

	firecracker string

	vmm string

	qemu string

	k3s string

	kubectl string

	kine string

	kcp string

	kvm string

	repo string

	postgresBinDir string

	retain int

	modules []string

	seeded []string

	configPath string

	configMissing []string

	password string

	inContainer bool
}

func inContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	for _, path := range []string{"/proc/self/cgroup", "/proc/1/cgroup"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, marker := range []string{"docker", "containerd", "kubepods", "libpod", "lxc"} {
			if strings.Contains(string(raw), marker) {
				return true
			}
		}
	}
	return false
}

func (f facts) absent() Kind {
	if f.inContainer {
		return PassedIn
	}
	return Missing
}

func (f facts) notHere(what string) string {
	if f.inContainer {
		return what + " is not in this container, and no container has it of its own: it has to be " +
			"handed in. "
	}
	return what + " is not on this host, and nothing else can stand in for it. "
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func kernelRelease() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "", fmt.Errorf("check-host: reading /proc/sys/kernel/osrelease: %w. Every path this "+
			"check compares is derived from the release this host is running -- the kernel a guest "+
			"boots is this host's own /boot/vmlinuz-$(uname -r) and the modules that can load in it "+
			"are the tree built beside that kernel -- so a host whose release cannot be read is a "+
			"host where every one of those comparisons is against a guess", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func readFacts(configPath string) (facts, error) {
	f := facts{
		configPath:  configPath,
		kvm:         "/dev/kvm",
		password:    envOr(envNodePassword, "/etc/rancher/node/password"),
		inContainer: inContainer(),
	}
	if hostos.Firecracker() {
		release, err := kernelRelease()
		if err != nil {
			return facts{}, err
		}
		f.release = release
		f.kernel = filepath.Join("/boot", "vmlinuz-"+release)
		f.kernelConfig = filepath.Join("/boot", "config-"+release)
		f.moduleSource = filepath.Join("/lib/modules", release)
	} else {
		f.release = hostos.Name()
	}
	if raw := os.Getenv(envRetain); raw != "" {
		n, err := parseRetain(raw)
		if err != nil {
			return facts{}, err
		}
		f.retain = n
	}
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			cfg, absent, err := loadNodeImageConfig(configPath)
			if err != nil {
				return facts{}, err
			}
			f.configMissing = absent
			f.vmm = cfg.VMM
			f.qemu = cfg.QEMU
			f.firecracker = cfg.Firecracker
			f.kernel = orDefault(cfg.KernelSource, f.kernel)
			f.kernelConfig = orDefault(cfg.KernelConfig, f.kernelConfig)
			f.moduleSource = orDefault(cfg.ModuleSource, f.moduleSource)
			f.stageDir = cfg.StageDir
			f.outDir = cfg.OutDir
			f.k3s = cfg.K3sBinary
			f.modules = cfg.Modules
			f.seeded = cfg.Images
		} else if !os.IsNotExist(err) {
			return facts{}, fmt.Errorf("check-host: reading %s: %w", configPath, err)
		} else {
			f.configMissing = append([]string{}, configKeys...)
		}
	} else {
		f.configMissing = append([]string{}, configKeys...)
	}
	f.firecracker = envOr(envFirecracker, f.firecracker)
	f.qemu = envOr(envQEMU, f.qemu)
	f.kernel = envOr(envKernel, f.kernel)
	f.kernelConfig = envOr(envKernelConfig, f.kernelConfig)
	f.moduleSource = envOr(envModuleSource, f.moduleSource)
	f.stageDir = envOr(envStageDir, f.stageDir)
	f.outDir = envOr(envOutDir, f.outDir)
	f.k3s = envOr(envK3s, f.k3s)
	f.kubectl = envOr(envKubectl, "")
	f.kine = envOr(envKine, "")
	f.kcp = envOr(envKCP, "/usr/local/bin/kcp")
	f.kvm = envOr(envKVM, f.kvm)
	f.postgresBinDir = envOr(envPostgresBinDir, "")
	wd, err := os.Getwd()
	if err != nil {
		return facts{}, fmt.Errorf("check-host: this process cannot name its own working directory: %w. "+
			"The SELinux check builds a copy of this binary inside the tree this project was "+
			"checked out into and executes it there, which is the shape that failed once already, "+
			"and a check that cannot name that tree cannot run the part of itself that reproduces it", err)
	}
	f.repo = envOr(envRepo, wd)
	if f.retain == 0 {
		f.retain = 3
	}
	return f, nil
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func parseRetain(raw string) (int, error) {
	n, err := parsePositive(raw)
	if err != nil {
		return 0, fmt.Errorf("check-host: %s=%q: %w. The number is how many images the store on "+
			"the output path is kept at, and the free space the checks below insist on is that many "+
			"images' worth: a budget read from a value this build cannot parse is a budget that "+
			"silently becomes zero, and a build told it has no room is a build that refuses to run "+
			"for a reason nobody can reproduce", envRetain, raw, err)
	}
	return n, nil
}

func parsePositive(raw string) (int, error) {
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a whole number", raw)
		}
		n = n*10 + int(r-'0')
		if n > 1_000_000 {
			return 0, fmt.Errorf("%q is larger than any store this project keeps", raw)
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("%q is not more than zero", raw)
	}
	return n, nil
}

func loadNodeImageConfig(path string) (nodeImageConfig, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nodeImageConfig{}, nil, fmt.Errorf("check-host: reading %s: %w", path, err)
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return nodeImageConfig{}, nil, fmt.Errorf("check-host: %s is not a JSON object: %w. The "+
			"paths below are the operator's, not this check's, and every one of them that cannot be "+
			"read is a path this check would otherwise guess at on the operator's behalf", path, err)
	}
	var cfg nodeImageConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nodeImageConfig{}, nil, fmt.Errorf("check-host: %s: %w", path, err)
	}
	var absent []string
	for _, key := range configKeys {
		if _, ok := present[key]; !ok {
			absent = append(absent, key)
		}
	}
	return cfg, absent, nil
}
