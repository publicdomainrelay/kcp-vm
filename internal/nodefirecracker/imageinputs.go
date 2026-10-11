package nodefirecracker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

const (
	digestPrefix = "sha256:"

	FingerprintPrefix = "sha256-"

	fingerprintHexDigits = 64

	moduleWhy = "The list is what the guest modprobes before k3s starts, and it resolves " +
		"against the module tree INSIDE this image, so a name the configuration carries and the " +
		"image does not is a modprobe that fails inside the guest: iptables in a guest does not " +
		"load extensions on demand, iptables-restore stops at the FIRST extension it cannot find, " +
		"the rules that precede it are applied and every rule after it is absent, kube-proxy " +
		"leaves 10.43.0.1:443 without an address and the network policy controller leaves the " +
		"cluster's pods with no firewall at all, and both say so only on the guest's own console, " +
		"which nobody reads until a Service has no address"

	initramfsWhy = "The initramfs is what runs before any root filesystem exists, so a module " +
		"named here and carried by no initramfs in this image is one the guest cannot load at the " +
		"only moment it needs it: a VMM hands the guest its block device over a virtio transport, " +
		"and a kernel that carries that transport as a module rather than built in leaves a guest " +
		"that cannot load it never seeing /dev/vda and panicking about a root device it never had"

	vmmWhy = "The VMM is what boots this image, and the transport it hands the guest its block " +
		"device over differs by VMM -- firecracker over virtio-mmio, qemu over virtio-pci. The " +
		"image records the VMM it was built for and the substrate refuses an image built for the " +
		"other one rather than booting it: the two boot the same kernel, so a cross-substrate boot " +
		"is not caught by the kernel, and the guest that results is one nobody described"

	imagesWhy = "The guest's containerd imports the tarballs this image carries and no others, " +
		"and nothing in the guest reaches for a registry, so a pod asking for an image nothing " +
		"seeded does not fail the way a missing image fails: it waits on a pull that never " +
		"completes, which is indistinguishable from a pull that is merely slow, and it never ends. " +
		"Each reference " +
		"is compared whole, digest included, because the configuration names every image as the " +
		"tag the guest asks for AND the digest that tag had resolved to at build time: a bare tag " +
		"is refused by the builder, since a tag re-pushed since the build leaves the reference " +
		"unchanged while the image it resolves to is a different one, and this comparison is " +
		"required to work on a host with no registry access at all, so a comparison that had to " +
		"ask a registry would be no comparison on the hosts this runs on"

	kernelWhy = "The guest boots the kernel this image already carries, extracted from that file " +
		"at build time, so a kernel replaced since then is one whose modules, whose netfilter " +
		"extensions and whose virtio transport the image carries only by coincidence: the image " +
		"holds the old kernel, the configuration now names the module tree built beside the new " +
		"one, and a module built for one kernel does not load into the other"

	kernelConfigWhy = "k3s's own check-config reads this file inside the guest to decide whether " +
		"the kernel can run k3s at all, so a config file replaced since the build is one that " +
		"describes a kernel other than the one the guest boots, and the check inside the guest " +
		"answers with confidence about the wrong kernel"

	baseImageWhy = "The root filesystem this image ships is that file unsquashed, so a base image " +
		"replaced since the build means every node booted from this image runs the PREVIOUS base's " +
		"userspace, its libraries and whatever the newer base fixed"

	k3sWhy = "The guest execs that binary as its server, so a k3s replaced since the build is a " +
		"node running a different k3s from the one the configuration names. This one matters more " +
		"than it looks with age rather than with edits: a k3s bump moves the add-on list AND the " +
		"images a cluster seeds together, so a node left on the old binary is one whose system " +
		"pods are the old cluster's, and nothing reports the difference because both clusters are " +
		"internally consistent"

	initWhy = "The guest's PID 1 is this binary, and an image carrying a different init from the " +
		"one in front of it boots a guest that behaves unlike the one this build describes. " +
		"THIS INPUT WAS MISSING AND THE FAILURE IT CAUSED IS THE REASON IT IS HERE. The builder " +
		"reported `up to date, nothing built, nothing pulled` and was right about every input it " +
		"tracked; the guest then ran the OLD binary -- the agent of that day, whose half is this " +
		"init's ancestor -- against the NEW files the image carried, read a removed field as Go's " +
		"zero value, passed `--kubelet-arg port=0`, and the kubelet refused to start at all -- " +
		"`port (--port) 0 must be between 1 and 65535` -- so the guest never joined. Nothing in " +
		"that sequence named the binary, and the operator's first instinct is to debug the kubelet: " +
		"a refusal that cannot name the thing that is actually stale is the failure mode, not a " +
		"missing check. The digest is taken from the binary the build just produced, not from the " +
		"source that produced it, because the flags and the toolchain are inputs to that binary and " +
		"no digest of a source tree covers either"

	moduleSourceWhy = "The build copies this tree into the image WHOLE, and it is what modprobe " +
		"inside the guest resolves against, so the tree is an input in its own right and not just " +
		"the names read out of it: two hosts holding the same kernel version can hold different " +
		"module trees, and the record used to weigh the names alone. The digest covers every file's " +
		"path and contents and every symlink's target, so pointing the configuration at another " +
		"tree of the same version moves the image's name, which is the only thing that makes the " +
		"difference visible without booting to find out"

	moduleSourceDigestWhy = "no digest of the tree it copied its modules from"

	busyboxWhy = "The initramfs carries this host's busybox as the insmod the guest's PID 1 loads " +
		"its transport module with, and the host it was copied from is the one build input that is " +
		"outside every other input: the kernel, the modules and the base image are all files a " +
		"configuration names, and this one is whatever the host happens to have installed. A host " +
		"that upgraded busybox since the build has an image that loads a module through a binary no " +
		"longer on the host, and nothing in a boot would name it"

	preinstallWhy = "The manifest is what decides which software is baked into the image, so what " +
		"the image carries is the manifest's: a manifest whose content changed names an image whose " +
		"packages were chosen by a list the configuration no longer holds, and a configuration that " +
		"stopped naming one describes an image carrying software it does not ask for -- in both " +
		"directions the guest installs at first boot what the image was supposed to have made " +
		"unnecessary, and the boot looks the same either way"

	bootArgsWhy = "The boot arguments become the kernel command line the guest is started with, and " +
		"they travel in the manifest inside the image, so a configuration that changed them and was " +
		"answered `up to date` leaves the image with the OLD command line: the operator edits the " +
		"file, sees no rebuild, and boots the previous boot"

	k3sDataDirWhy = "The guest's k3s writes its state, its manifests and its seeded image tarballs " +
		"under this directory, so an image built for one data directory and booted with another is a " +
		"guest whose containerd and k3s disagree about where their state lives"

	rootfsLabelWhy = "The label is how the guest's init and the host's tooling recognise this " +
		"filesystem, and an image carrying one label booted as a root device labelled differently is " +
		"a root device something else may claim"

	rootfsHeadroomWhy = "The headroom is what decides how much space the image has beyond its " +
		"staging tree, so an image built with one figure and used as though it had another is a node " +
		"that fills its disk at a moment nothing predicted"

	initPackageWhy = "The package is what the guest's init is built FROM, so a configuration " +
		"naming another package is a configuration this image was not built from, whatever the " +
		"binary it produced happens to look like -- and a package is a directory of source rather " +
		"than a file, so an edited init under an unchanged path is a different init again, and the " +
		"binary the image carries is then not the one this configuration would build"

	initPackageDigestWhy = "no digest of the init's own source, so the source the binary in this " +
		"image was built from cannot be compared against the package the configuration names"

	initGoFlagsWhy = "The flags are inputs to the init's binary -- trimming, symbols, and anything " +
		"added later -- so two builds of one package under different flags are two different inits " +
		"and this image carries one of them"
)

var ErrNoBuildInputs = errors.New(
	"nodefirecracker: the image manifest records nothing about the inputs it was built from, so " +
		"the question this check exists to answer -- is the image in this directory the one the " +
		"configuration in front of me describes -- cannot be answered at all. An unanswerable " +
		"provenance is refused rather than assumed, and this is the refusal that pays for itself: " +
		"an image built before a configuration gained kernel modules boots a guest whose firewall " +
		"is missing exactly those extensions, and neither the boot, nor the join, nor any object " +
		"in the cluster names the image as the cause -- the symptom is a Service with no address, " +
		"and the only way to see it is to read the module count out of the image by hand. Rebuild " +
		"the image (socialweb-nodeimage -config <the configuration>) so that it carries this record")

type InputsDifferError struct {
	ConfigPath string

	Differences []string
}

func (e *InputsDifferError) Error() string {
	return fmt.Sprintf("nodefirecracker: the image in this directory was not built from the "+
		"configuration at %s. Every difference below is one a guest booted from this image would "+
		"carry into the cluster, and none of them is visible after the boot: the guest comes up, "+
		"the node joins and its phase reaches Running, and the image it came from is named nowhere "+
		"in any object anything reads.\n\n%s\n\nRebuild the image from this configuration "+
		"(socialweb-nodeimage -config %s). It is refused rather than rebuilt here: a build pulls "+
		"its seeded images over the network and takes minutes, and a controller that built one "+
		"would be assembling a node's filesystem as a side effect of deciding to place a node",
		e.ConfigPath, strings.Join(e.Differences, "\n\n"), e.ConfigPath)
}

type BuildConfig struct {
	VMM string `json:"vmm"`

	Modules []string `json:"modules"`

	InitramfsModules []string `json:"initramfs_modules"`

	Images []string `json:"images"`

	KernelSource string `json:"kernel_source"`

	KernelConfig string `json:"kernel_config"`

	BaseImage string `json:"base_image"`

	K3sBinary string `json:"k3s_binary"`

	InitBinary string `json:"init_binary"`

	InitPackage string `json:"init_package"`

	InitGoFlags []string `json:"init_go_flags"`

	ModuleSource string `json:"module_source"`

	BootArgs []string `json:"boot_args"`

	K3sDataDir string `json:"k3s_data_dir"`

	RootfsLabel string `json:"rootfs_label"`

	RootfsHeadroomMiB int `json:"rootfs_headroom_mib"`

	Vcpu int `json:"vcpu"`

	MemMiB int `json:"memMiB"`

	Preinstall string `json:"preinstall"`
}

type BuildInputs struct {
	ConfigPath string `json:"configPath"`

	BuilderDigest string `json:"builderDigest"`

	VMM string `json:"vmm"`

	Modules []string `json:"modules"`

	InitramfsModules []string `json:"initramfsModules"`

	Images []string `json:"images"`

	KernelSource string `json:"kernelSource"`

	KernelSourceDigest string `json:"kernelSourceDigest"`

	KernelConfig string `json:"kernelConfig"`

	KernelConfigDigest string `json:"kernelConfigDigest"`

	BaseImage string `json:"baseImage"`

	BaseImageDigest string `json:"baseImageDigest"`

	K3sBinary string `json:"k3sBinary"`

	K3sBinaryDigest string `json:"k3sBinaryDigest"`

	Busybox string `json:"busybox"`

	BusyboxDigest string `json:"busyboxDigest"`

	InitBinary string `json:"initBinary"`

	InitBinaryDigest string `json:"initBinaryDigest"`

	InitPackageDigest string `json:"initPackageDigest"`

	InitPackage string `json:"initPackage"`

	InitGoFlags []string `json:"initGoFlags"`

	ModuleSource string `json:"moduleSource"`

	ModuleSourceDigest string `json:"moduleSourceDigest"`

	BootArgs []string `json:"bootArgs"`

	K3sDataDir string `json:"k3sDataDir"`

	RootfsLabel string `json:"rootfsLabel"`

	RootfsHeadroomMiB int `json:"rootfsHeadroomMiB"`

	Vcpu int `json:"vcpu"`

	MemMiB int `json:"memMiB"`

	Preinstall string `json:"preinstall"`

	PreinstallDigest string `json:"preinstallDigest"`
}

func (i BuildInputs) Fingerprint() (string, error) {
	raw, err := json.Marshal(i)
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: rendering the record of what a build read: %w", err)
	}
	sum := sha256.Sum256(raw)
	return FingerprintPrefix + hex.EncodeToString(sum[:]), nil
}

func IsFingerprint(name string) bool {
	hex := strings.TrimPrefix(name, FingerprintPrefix)
	if len(hex) != fingerprintHexDigits {
		return false
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func resolvePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: resolving the path %s, which is part of the name a "+
			"build of it is given: %w -- a relative path names a different file for every working "+
			"directory the reader happened to run in, and two names for one configuration are two "+
			"images of 2.3 GB in a store that is meant to hold one", path, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", unresolvedPathRefusal(path, err)
	}
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(absolute))
	if parentErr != nil {
		return "", unresolvedPathRefusal(path, err)
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func moduleRoot(initPackage string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: reading the working directory, which is where go "+
			"starts looking for the module a package path is written relative to: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("nodefirecracker: looking for a go.mod in %s, which is one of the "+
				"directories a package path is written relative to: %w -- a package whose module "+
				"cannot be read is one whose source cannot be found, and a build input that cannot "+
				"be found is one no later check can hold this image to", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("nodefirecracker: init_package is %q, which is a path relative "+
				"to the module the init is built in, and there is no go.mod in the working directory "+
				"or in any directory above it. That path is what go resolves when it builds the "+
				"package, and go resolves it from the module rather than from the directory the "+
				"command happened to be run in -- so from here there is nothing for it to be "+
				"relative to, and resolving it against the working directory instead would record "+
				"the digest of whatever tree is standing there. Run this from inside the checkout, "+
				"or give init_package as the absolute path of the package", initPackage)
		}
		dir = parent
	}
}

func ResolveInitPackage(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("nodefirecracker: the configuration names no init_package, and the " +
			"init is the guest's PID 1, built from that package. A build refuses a " +
			"configuration without it, so an empty one is not a configuration with no init -- it is " +
			"a field that has been renamed or dropped, and the empty string resolves to the working " +
			"directory, which is a directory that exists and digests: the record would then hold the " +
			"digest of whatever tree the reader happened to be standing in, and a change to the " +
			"init's own source would move nothing")
	}
	if filepath.IsAbs(path) {
		return resolvePath(path)
	}
	root, err := moduleRoot(path)
	if err != nil {
		return "", err
	}
	return resolvePath(filepath.Join(root, path))
}

func unresolvedPathRefusal(path string, cause error) error {
	return fmt.Errorf("nodefirecracker: resolving the path %s, which is part of the name an image "+
		"built from it is given and part of the record a later boot holds that image to: %w. One "+
		"file reached through a symlink and reached directly is one file, so a name that follows "+
		"the spelling instead of the file gives one file two names, and the store then holds two "+
		"copies of the same 2.3 GB image under both -- the second built for a change nothing "+
		"made. A path that cannot be resolved is also the one case where a stale image and a "+
		"current one look identical, so this is refused rather than recorded as the spelling it "+
		"was written with, and no name is handed out for it", path, cause)
}

func Fingerprint(configPath string, cfg BuildConfig) (string, error) {
	inputs, err := RecordInputs(configPath, cfg)
	if err != nil {
		return "", err
	}
	return inputs.Fingerprint()
}

func canonicalSet(names []string) []string {
	canonical := append([]string(nil), names...)
	slices.Sort(canonical)
	return slices.Compact(canonical)
}

func LoadBuildConfig(path string) (BuildConfig, error) {
	return loadBuildConfig(path, HostKernelResolver())
}

func loadBuildConfig(path string, resolver KernelResolver) (BuildConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return BuildConfig{}, fmt.Errorf("nodefirecracker: reading the image builder's "+
			"configuration at %s, which is the configuration the image in this directory says it "+
			"was built from: %w -- a configuration that cannot be read is one no image can be "+
			"shown to match, and an image whose provenance cannot be shown is not booted on the "+
			"strength of it having been readable last time", path, err)
	}
	var cfg BuildConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return BuildConfig{}, fmt.Errorf("nodefirecracker: %s: %w", path, err)
	}
	if err := RefuseRenamedKeys(raw, path); err != nil {
		return BuildConfig{}, err
	}
	vmm, err := nodevm.NormalizeVMM(cfg.VMM)
	if err != nil {
		return BuildConfig{}, fmt.Errorf("nodefirecracker: the image builder's configuration at %s: %w",
			path, err)
	}
	cfg.VMM = vmm
	for _, named := range []struct {
		key   string
		empty bool
	}{
		{"modules", len(cfg.Modules) == 0},
		{"initramfs_modules", len(cfg.InitramfsModules) == 0},
		{"images", len(cfg.Images) == 0},
		{"kernel_source", cfg.KernelSource == ""},
		{"kernel_config", cfg.KernelConfig == ""},
		{"base_image", cfg.BaseImage == ""},
		{"k3s_binary", cfg.K3sBinary == ""},
		{"init_binary", cfg.InitBinary == ""},
		{"module_source", cfg.ModuleSource == ""},
	} {
		if !named.empty {
			continue
		}
		return BuildConfig{}, fmt.Errorf("nodefirecracker: the image builder's configuration at "+
			"%s names no %s. Every one of these is required by the builder itself, which refuses "+
			"a configuration without it, so a configuration that has none of them is either not "+
			"the file that built this image or a file whose key has been renamed -- and either way "+
			"the comparison this check exists to make cannot be made against a field that reads "+
			"empty. An absent input compared against a present one is a difference the check "+
			"would report as the image's, which sends the operator to rebuild an image that may "+
			"be the correct one", path, named.key)
	}
	resolution, err := resolver.Resolve(cfg.KernelSource, cfg.KernelConfig, cfg.ModuleSource)
	if err != nil {
		return BuildConfig{}, err
	}
	cfg.KernelSource = resolution.Source
	cfg.KernelConfig = resolution.Config
	cfg.ModuleSource = resolution.Modules
	return cfg, nil
}

var renamedImageConfigKeys = map[string]string{
	"agent_package":  "init_package",
	"agent_binary":   "init_binary",
	"agent_go_flags": "init_go_flags",
}

func RefuseRenamedKeys(raw []byte, path string) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("nodefirecracker: %s: %w", path, err)
	}
	renamed := make([]string, 0, len(renamedImageConfigKeys))
	for old, new := range renamedImageConfigKeys {
		if _, ok := keys[old]; !ok {
			continue
		}
		renamed = append(renamed, fmt.Sprintf("%s -> %s", old, new))
	}
	if len(renamed) == 0 {
		return nil
	}
	sort.Strings(renamed)
	return fmt.Errorf("nodefirecracker: the image builder's configuration at %s carries the "+
		"retired key(s) %s. The guest binary these name is the guest's init and nothing else "+
		"(plan 0005 M1-AGENT-SPLIT), so the old spellings are refused rather than read: a build "+
		"that accepted one would fingerprint an image by a key the current builder no longer "+
		"writes, and the same configuration would name two different images depending on which "+
		"builder read it", path, strings.Join(renamed, ", "))
}

func RecordInputs(configPath string, cfg BuildConfig) (BuildInputs, error) {
	return recordInputs(configPath, cfg, ExecutableDigest)
}

func ExecutableDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: finding the builder whose own digest is recorded, "+
			"because the name an image is given is the hash of everything its build read and of the "+
			"builder that read it: %w -- a builder that cannot name itself cannot say whether the image "+
			"already in the store is one it wrote, and the answer that costs is the one where a change "+
			"to the builder keeps the name and the image from before the change is shipped under it", err)
	}
	digest, err := fileDigest(path)
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: reading %s, the builder whose own digest is recorded, "+
			"so that a store holding an image an earlier builder wrote cannot answer for this one: %w -- "+
			"the builder's behaviour is an input of what it produces and is not written in any "+
			"configuration, so nothing else here can stand in for it", path, err)
	}
	return digest, nil
}

func recordInputs(configPath string, cfg BuildConfig, builderDigest func() (string, error)) (BuildInputs, error) {
	resolvedConfig, err := resolvePath(configPath)
	if err != nil {
		return BuildInputs{}, err
	}
	digest, err := builderDigest()
	if err != nil {
		return BuildInputs{}, err
	}
	inputs := BuildInputs{
		ConfigPath:        resolvedConfig,
		BuilderDigest:     digest,
		Modules:           canonicalSet(cfg.Modules),
		InitramfsModules:  canonicalSet(cfg.InitramfsModules),
		Images:            canonicalSet(cfg.Images),
		InitPackage:       cfg.InitPackage,
		InitGoFlags:       append([]string(nil), cfg.InitGoFlags...),
		BootArgs:          append([]string(nil), cfg.BootArgs...),
		K3sDataDir:        cfg.K3sDataDir,
		RootfsLabel:       cfg.RootfsLabel,
		RootfsHeadroomMiB: cfg.RootfsHeadroomMiB,
		Vcpu:              cfg.Vcpu,
		MemMiB:            cfg.MemMiB,
	}
	vmm, err := nodevm.NormalizeVMM(cfg.VMM)
	if err != nil {
		return BuildInputs{}, err
	}
	inputs.VMM = vmm
	for _, f := range []struct {
		what  string
		path  string
		named *string
		into  *string
	}{
		{"kernel source", cfg.KernelSource, &inputs.KernelSource, &inputs.KernelSourceDigest},
		{"kernel configuration", cfg.KernelConfig, &inputs.KernelConfig, &inputs.KernelConfigDigest},
		{"base image", cfg.BaseImage, &inputs.BaseImage, &inputs.BaseImageDigest},
		{"k3s binary", cfg.K3sBinary, &inputs.K3sBinary, &inputs.K3sBinaryDigest},
		{"host busybox", HostBusybox, &inputs.Busybox, &inputs.BusyboxDigest},
	} {
		resolved, resolveErr := resolvePath(f.path)
		digest := ""
		err := resolveErr
		if err == nil {
			digest, err = fileDigest(resolved)
		}
		if err != nil {
			return BuildInputs{}, fmt.Errorf("nodefirecracker: reading the %s at %s, whose identity "+
				"is recorded so that a later boot can tell whether the file it names is still the "+
				"file this build read: %w -- an input that cannot be fingerprinted at build time is "+
				"one no later check can hold this image to, and the whole point of recording the "+
				"inputs is that a change to any of them is visible without rebuilding to find out",
				f.what, f.path, err)
		}
		*f.named = resolved
		*f.into = digest
	}
	if strings.TrimSpace(cfg.Preinstall) != "" {
		resolved, err := resolvePath(cfg.Preinstall)
		if err != nil {
			return BuildInputs{}, err
		}
		digest, err := fileDigest(resolved)
		if err != nil {
			return BuildInputs{}, fmt.Errorf("nodefirecracker: reading the preinstall manifest at %s, "+
				"which decides which software is baked into the image and is therefore part of what "+
				"the image is: %w -- a preinstall list that changed while the name stayed the same "+
				"would be a guest booted from an image carrying the software from before the change, "+
				"and nothing downstream would name the manifest as the cause", cfg.Preinstall, err)
		}
		inputs.Preinstall = resolved
		inputs.PreinstallDigest = digest
	}
	inputs.InitBinary = ""
	inputs.InitBinaryDigest = ""
	if _, statErr := os.Stat(cfg.InitBinary); statErr == nil {
		resolvedBinary, err := resolvePath(cfg.InitBinary)
		if err != nil {
			return BuildInputs{}, err
		}
		digest, err := fileDigest(resolvedBinary)
		if err != nil {
			return BuildInputs{}, fmt.Errorf("nodefirecracker: reading the guest init at %s: %w",
				cfg.InitBinary, err)
		}
		inputs.InitBinary = resolvedBinary
		inputs.InitBinaryDigest = digest
	} else if !os.IsNotExist(statErr) {
		return BuildInputs{}, fmt.Errorf("nodefirecracker: looking for the guest init at %s: %w",
			cfg.InitBinary, statErr)
	}
	resolvedInit, err := ResolveInitPackage(cfg.InitPackage)
	if err != nil {
		return BuildInputs{}, err
	}
	initTree, err := treeDigest(resolvedInit)
	if err != nil {
		return BuildInputs{}, fmt.Errorf("nodefirecracker: reading the guest init's own source at %s, "+
			"which is recorded so that a source change is visible without a Go toolchain to build the "+
			"binary with: %w -- the binary is what the image carries and this is what the binary is "+
			"built from, and a build that had to happen before the question could be asked would make "+
			"asking it impossible on a host that has no toolchain", cfg.InitPackage, err)
	}
	inputs.InitPackage = resolvedInit
	inputs.InitPackageDigest = initTree
	resolvedSource, err := resolvePath(cfg.ModuleSource)
	if err != nil {
		return BuildInputs{}, err
	}
	tree, err := treeDigest(resolvedSource)
	if err != nil {
		return BuildInputs{}, fmt.Errorf("nodefirecracker: reading the module tree at %s, which the "+
			"build copies into the image whole and which therefore decides what modprobe can load "+
			"inside the guest: %w -- an input that cannot be fingerprinted at build time is one no "+
			"later check can hold this image to", cfg.ModuleSource, err)
	}
	inputs.ModuleSource = resolvedSource
	inputs.ModuleSourceDigest = tree
	return inputs, nil
}

func treeDigest(root string) (string, error) {
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return nil
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			lines = append(lines, rel+" -> "+target)
			return nil
		}
		digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		lines = append(lines, rel+" "+digest)
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.Sort(lines)
	sum := sha256.New()
	for _, line := range lines {
		sum.Write([]byte(line + "\n"))
	}
	return digestPrefix + hex.EncodeToString(sum.Sum(nil)), nil
}

func (m Manifest) CheckInputs() error {
	if m.Inputs == nil {
		return ErrNoBuildInputs
	}
	cfg, err := LoadBuildConfig(m.Inputs.ConfigPath)
	if err != nil {
		return err
	}
	return CompareImageToConfig(m, m.Inputs.ConfigPath, cfg)
}

func CompareImageToConfig(m Manifest, configPath string, cfg BuildConfig) error {
	if m.Inputs == nil {
		return ErrNoBuildInputs
	}
	refs := make([]string, 0, len(m.Images))
	for _, img := range m.Images {
		refs = append(refs, img.Reference)
	}
	diffs := []string{}
	recordedVMM, err := nodevm.NormalizeVMM(m.Inputs.VMM)
	if err != nil {
		return err
	}
	namedVMM, err := nodevm.NormalizeVMM(cfg.VMM)
	if err != nil {
		return err
	}
	if recordedVMM != namedVMM {
		diffs = append(diffs, fmt.Sprintf("the VMM this image was built for: this image records "+
			"%s and the configuration now names %s. %s", recordedVMM, namedVMM, vmmWhy))
	}
	if d := listDifference("kernel modules", m.Modules, cfg.Modules, moduleWhy); d != "" {
		diffs = append(diffs, d)
	}
	if d := listDifference("initramfs modules", m.Inputs.InitramfsModules, cfg.InitramfsModules, initramfsWhy); d != "" {
		diffs = append(diffs, d)
	}
	if d := listDifference("seeded images", refs, cfg.Images, imagesWhy); d != "" {
		diffs = append(diffs, d)
	}
	for _, f := range []struct {
		what     string
		recorded string
		digest   string
		named    string
		why      string
	}{
		{"kernel", m.Inputs.KernelSource, m.Inputs.KernelSourceDigest, cfg.KernelSource, kernelWhy},
		{"kernel configuration", m.Inputs.KernelConfig, m.Inputs.KernelConfigDigest, cfg.KernelConfig, kernelConfigWhy},
		{"base image", m.Inputs.BaseImage, m.Inputs.BaseImageDigest, cfg.BaseImage, baseImageWhy},
		{"k3s binary", m.K3sBinary, m.Inputs.K3sBinaryDigest, cfg.K3sBinary, k3sWhy},
		{"guest init", m.Inputs.InitBinary, m.Inputs.InitBinaryDigest, cfg.InitBinary, initWhy},
		{"host busybox", m.Inputs.Busybox, m.Inputs.BusyboxDigest, HostBusybox, busyboxWhy},
	} {
		named, err := resolvePath(f.named)
		if err != nil {
			return err
		}
		recorded := f.recorded
		if resolved, err := resolvePath(f.recorded); err == nil {
			recorded = resolved
		}
		d, err := fileDifference(f.what, recorded, f.digest, named, f.why)
		if err != nil {
			return err
		}
		if d != "" {
			diffs = append(diffs, d)
		}
	}
	if d, err := optionalFileDifference("preinstall manifest", m.Inputs.Preinstall,
		m.Inputs.PreinstallDigest, cfg.Preinstall, preinstallWhy); err != nil {
		return err
	} else if d != "" {
		diffs = append(diffs, d)
	}
	for _, t := range []struct {
		what     string
		recorded string
		digest   string
		named    string
		emptyWhy string
		why      string
		resolve  func(string) (string, error)
	}{
		{"kernel modules' tree", m.Inputs.ModuleSource, m.Inputs.ModuleSourceDigest, cfg.ModuleSource,
			moduleSourceDigestWhy, moduleSourceWhy, resolvePath},
		{"init package's tree", m.Inputs.InitPackage, m.Inputs.InitPackageDigest, cfg.InitPackage,
			initPackageDigestWhy, initPackageWhy, ResolveInitPackage},
	} {
		recorded := t.recorded
		if resolved, err := resolvePath(t.recorded); err == nil {
			recorded = resolved
		}
		named, err := t.resolve(t.named)
		if err != nil {
			return err
		}
		if d, err := treeDifference(t.what, recorded, t.digest, named, t.emptyWhy, t.why); err != nil {
			return err
		} else if d != "" {
			diffs = append(diffs, d)
		}
	}
	if d := listDifference("boot arguments", m.Inputs.BootArgs, cfg.BootArgs, bootArgsWhy); d != "" {
		diffs = append(diffs, d)
	}
	if d := listDifference("init build flags", m.Inputs.InitGoFlags, cfg.InitGoFlags, initGoFlagsWhy); d != "" {
		diffs = append(diffs, d)
	}
	for _, f := range []struct {
		what     string
		recorded string
		named    string
		why      string
	}{
		{"guest's k3s data directory", m.Inputs.K3sDataDir, cfg.K3sDataDir, k3sDataDirWhy},
		{"rootfs label", m.Inputs.RootfsLabel, cfg.RootfsLabel, rootfsLabelWhy},
	} {
		if f.recorded == f.named {
			continue
		}
		diffs = append(diffs, fmt.Sprintf("%s: this image was built with %q and the configuration now "+
			"names %q. %s", f.what, f.recorded, f.named, f.why))
	}
	if m.Inputs.RootfsHeadroomMiB != cfg.RootfsHeadroomMiB {
		diffs = append(diffs, fmt.Sprintf("rootfs headroom: this image was built with %d MiB of "+
			"headroom and the configuration now names %d. %s", m.Inputs.RootfsHeadroomMiB,
			cfg.RootfsHeadroomMiB, rootfsHeadroomWhy))
	}
	if len(diffs) == 0 {
		return nil
	}
	return &InputsDifferError{ConfigPath: configPath, Differences: diffs}
}

func treeDifference(what, recorded, digest, named, emptyWhy, why string) (string, error) {
	if digest == "" {
		return fmt.Sprintf("%s: this image records %s, so the tree it was built from cannot be "+
			"compared against %s. %s", what, emptyWhy, named, why), nil
	}
	if recorded != named {
		return fmt.Sprintf("%s: this image was built from %s and the configuration now names %s. %s",
			what, recorded, named, why), nil
	}
	current, err := treeDigest(named)
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: reading the tree the configuration names at "+
			"%s, which is the tree this image records being built from: %w -- whether this image "+
			"was built from the configuration in front of it cannot be established from here, and "+
			"this refuses rather than passing on an input it could not compare", named, err)
	}
	if current != digest {
		return fmt.Sprintf("%s: this image records the tree at %s as %s and this host's tree of the "+
			"same name is %s, so the files this image was built from are not the files the "+
			"configuration names. %s", what, named, digest, current, why), nil
	}
	return "", nil
}

func listDifference(what string, recorded, named []string, why string) string {
	added := onlyIn(named, recorded)
	removed := onlyIn(recorded, named)
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	lines := []string{fmt.Sprintf("%s: this image was built with %d and the configuration now "+
		"names %d.", what, len(recorded), len(named))}
	if len(added) > 0 {
		lines = append(lines, fmt.Sprintf("Named by the configuration and absent from this image: "+
			"%s.", strings.Join(added, ", ")))
	}
	if len(removed) > 0 {
		lines = append(lines, fmt.Sprintf("Carried by this image and no longer named by the "+
			"configuration: %s.", strings.Join(removed, ", ")))
	}
	lines = append(lines, why)
	return strings.Join(lines, " ")
}

func fileDifference(what, recorded, digest, named, why string) (string, error) {
	if named != recorded {
		return fmt.Sprintf("%s: this image was built from %s and the configuration now names %s. %s",
			what, recorded, named, why), nil
	}
	current, err := fileDigest(named)
	if err != nil {
		return "", fmt.Errorf("nodefirecracker: reading the %s the configuration names at %s, "+
			"which is the file this image records being built from: %w -- whether this image was "+
			"built from the configuration in front of it cannot be established from here, and this "+
			"refuses rather than passing on an input it could not compare: an unreadable input is "+
			"the one case where a stale image and a current one look identical", what, named, err)
	}
	if current == digest {
		return "", nil
	}
	return fmt.Sprintf("%s: the file at %s is not the file this image was built from. It is now "+
		"%s and this image records %s. %s", what, named, current, digest, why), nil
}

func optionalFileDifference(what, recorded, digest, named, why string) (string, error) {
	if recorded == "" && named == "" {
		return "", nil
	}
	if recorded == "" {
		return fmt.Sprintf("%s: this image was built without one and the configuration names %s. %s",
			what, named, why), nil
	}
	if named == "" {
		return fmt.Sprintf("%s: this image was built from %s and the configuration names none. %s",
			what, recorded, why), nil
	}
	resolvedRecorded := recorded
	if resolved, err := resolvePath(recorded); err == nil {
		resolvedRecorded = resolved
	}
	resolvedNamed, err := resolvePath(named)
	if err != nil {
		return "", err
	}
	return fileDifference(what, resolvedRecorded, digest, resolvedNamed, why)
}

func onlyIn(these, those []string) []string {
	out := []string{}
	for _, one := range these {
		if !slices.Contains(those, one) {
			out = append(out, one)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return digestPrefix + hex.EncodeToString(sum.Sum(nil)), nil
}
