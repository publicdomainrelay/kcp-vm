package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kernelmodules"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

type Config struct {
	VMM string `json:"vmm"`

	Firecracker string `json:"firecracker"`

	QEMU string `json:"qemu"`

	KernelSource string `json:"kernel_source"`

	KernelConfig string `json:"kernel_config"`

	ModuleSource string `json:"module_source"`

	BaseImage string `json:"base_image"`

	StageDir string `json:"stage_dir"`

	OutDir string `json:"out_dir"`

	AgentPackage string `json:"agent_package"`

	AgentBinary string `json:"agent_binary"`

	AgentGoFlags []string `json:"agent_go_flags"`

	K3sBinary string `json:"k3s_binary"`

	K3sDataDir string `json:"k3s_data_dir"`

	Images []string `json:"images"`

	ImageCacheDir string `json:"image_cache_dir"`

	RootfsLabel string `json:"rootfs_label"`

	RootfsHeadroomMiB int `json:"rootfs_headroom_mib"`

	Vcpu int `json:"vcpu"`

	MemMiB int `json:"memMiB"`

	BootArgs []string `json:"boot_args"`

	Modules []string `json:"modules"`

	InitramfsModules []string `json:"initramfs_modules"`

	Preinstall string `json:"preinstall"`

	Path string `json:"-"`

	KernelOrigin string `json:"-"`

	KernelVersion string `json:"-"`
}

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("socialweb-nodeimage: reading %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("socialweb-nodeimage: %s: %w", path, err)
	}
	root := filepath.Dir(path)
	for _, p := range []*string{&cfg.AgentBinary, &cfg.StageDir, &cfg.OutDir, &cfg.ImageCacheDir, &cfg.Preinstall} {
		if *p == "" || filepath.IsAbs(*p) {
			continue
		}
		*p = filepath.Join(root, *p)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("socialweb-nodeimage: resolving %s: %w -- the image records the "+
			"configuration it was built from, and a substrate on this host reads that record back "+
			"to compare this file against the image before it boots a node; a relative path there "+
			"would name a different file for every working directory the reader happened to run in, "+
			"and a comparison against the wrong file is a pass that means nothing", path, err)
	}
	cfg.Path = absolute
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	resolution, err := nodefirecracker.HostKernelResolver().Resolve(cfg.KernelSource, cfg.KernelConfig, cfg.ModuleSource)
	if err != nil {
		return Config{}, err
	}
	cfg.KernelSource = resolution.Source
	cfg.KernelConfig = resolution.Config
	cfg.ModuleSource = resolution.Modules
	cfg.KernelOrigin = string(resolution.Origin)
	cfg.KernelVersion = resolution.Version
	if resolution.Origin != nodefirecracker.KernelNamed {
		fmt.Printf("kernel %s %s\n  source  %s\n  config  %s\n  modules %s\n",
			resolution.Version, resolution.Origin, resolution.Source, resolution.Config, resolution.Modules)
	}
	return cfg, nil
}

func (c Config) VMMName() string {
	if c.VMM == "" {
		return nodevm.VMMFirecracker
	}
	return c.VMM
}

func (c Config) Validate() error {
	vmm, err := nodevm.NormalizeVMM(c.VMM)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	required := []struct{ name, value string }{
		{"kernel_source", c.KernelSource},
		{"kernel_config", c.KernelConfig},
		{"module_source", c.ModuleSource},
		{"base_image", c.BaseImage},
		{"stage_dir", c.StageDir},
		{"out_dir", c.OutDir},
		{"agent_package", c.AgentPackage},
		{"agent_binary", c.AgentBinary},
		{"k3s_binary", c.K3sBinary},
		{"k3s_data_dir", c.K3sDataDir},
		{"image_cache_dir", c.ImageCacheDir},
	}
	if vmm == nodevm.VMMQEMU {
		required = append(required, struct{ name, value string }{"qemu", c.QEMU})
	} else {
		required = append(required, struct{ name, value string }{"firecracker", c.Firecracker})
	}
	for _, req := range required {
		if strings.TrimSpace(req.value) == "" {
			return fmt.Errorf("socialweb-nodeimage: %s is not set in the config, and an image "+
				"builder that guessed it would bake a guest that boots with a path nothing wrote",
				req.name)
		}
	}
	if len(c.Images) == 0 {
		return fmt.Errorf("socialweb-nodeimage: images is empty. That list is not optional. The " +
			"guest this builder produces has no resolver, so every image its kubelet wants has to " +
			"already be in the guest's filesystem as a tarball k3s's own agent imports before it " +
			"starts the kubelet; with nothing to import, the cluster's system components stay " +
			"Pending forever and nothing in the guest's console says why, because a pull that " +
			"cannot resolve a hostname looks exactly like a pull that is merely slow")
	}
	seen := make(map[string]struct{}, len(c.Images))
	seeded := make(map[string]string, len(c.Images))
	for _, image := range c.Images {
		if strings.TrimSpace(image) == "" {
			return fmt.Errorf("socialweb-nodeimage: images carries an empty entry. The entries " +
				"become both the references this builder pulls and the file names the guest " +
				"imports, and an empty one is a refusal from the registry rather than from here")
		}
		if _, dup := seen[image]; dup {
			return fmt.Errorf("socialweb-nodeimage: images names %s twice. The list is the set of "+
				"images the guest's containerd imports, and a second copy of one image is a second "+
				"copy written into the guest's filesystem and a second copy unpacked into its "+
				"store, spending headroom that the guest needs to be able to write anything at all",
				image)
		}
		seen[image] = struct{}{}
		one, err := parseImageReference(image)
		if err != nil {
			return err
		}
		key := image
		if one.hasTag {
			key = one.tag.Name()
		}
		if first, dup := seeded[key]; dup {
			return fmt.Errorf("socialweb-nodeimage: images names %s and %s, which are seeded under "+
				"one name. What the guest's containerd holds is the tag: it is what every k3s "+
				"manifest asks for and what a pod is matched against, so two entries under one tag "+
				"are two images competing for one name, and which of them the guest ends up running "+
				"depends on the order their tarballs are imported -- which nothing here controls and "+
				"nothing reports. Both the tag and the digest are inputs the image's name is made "+
				"of, so if both digests are wanted, they need two tags", first, image)
		}
		seeded[key] = image
	}
	for _, inside := range []struct{ name, dir string }{
		{"stage_dir", c.StageDir},
		{"out_dir", c.OutDir},
	} {
		if inside.dir == "" {
			continue
		}
		if within(c.ImageCacheDir, inside.dir) {
			return fmt.Errorf("socialweb-nodeimage: image_cache_dir is %s, inside %s at %s. The "+
				"cache holds the pulled image tarballs and is not part of any image: the stage is "+
				"wiped and rebuilt on every build, and the output directory is what the substrate "+
				"boots, so a cache kept in either is re-pulled from nothing on every build or is "+
				"baked into the guest as a second copy of every image it was seeded from",
				c.ImageCacheDir, inside.name, inside.dir)
		}
	}
	if len(c.BootArgs) == 0 {
		return fmt.Errorf("socialweb-nodeimage: boot_args is empty, so the kernel would be told " +
			"nothing about its console or its root device")
	}
	if len(c.Modules) == 0 {
		return fmt.Errorf("socialweb-nodeimage: modules is empty. That list is not a preference: " +
			"a kernel whose netfilter modules are not loaded fails k3s's own check-config, and it " +
			"fails there rather than at the first Service, which is far from the cause")
	}
	if len(c.InitramfsModules) == 0 {
		return fmt.Errorf("socialweb-nodeimage: initramfs_modules is empty. The initramfs is what " +
			"runs before any root filesystem exists, so a transport the guest's block device " +
			"arrives on and the kernel carries as a module has to be loadable there: a guest that " +
			"cannot load it never sees /dev/vda and panics about a root device instead")
	}
	return nil
}

type vmmTransport struct {
	vmm string

	module string
}

func vmmTransports() []vmmTransport {
	return []vmmTransport{
		{vmm: nodevm.VMMFirecracker, module: "kernel/drivers/virtio/virtio_mmio"},
		{vmm: nodevm.VMMQEMU, module: "kernel/drivers/virtio/virtio_pci"},
	}
}

func checkVMMTransport(c Config) error {
	tree, err := kernelmodules.LoadTree(c.ModuleSource)
	if err != nil {
		return err
	}
	selected := c.VMMName()
	for _, t := range vmmTransports() {
		if t.vmm != selected {
			continue
		}
		name := filepath.Base(t.module)
		if tree.BuiltIn(name) {
			return nil
		}
		if !tree.Loadable(name) {
			return fmt.Errorf("socialweb-nodeimage: the %s VMM hands the guest its block device "+
				"over %s, and the module tree at %s carries neither a built-in %s nor a loadable "+
				"one. An image built here would boot a guest that never sees /dev/vda and panics "+
				"about a root device, and nothing in the guest would name this tree as the cause. "+
				"Point module_source at the tree built beside the kernel this image boots, or "+
				"choose the VMM whose transport this kernel does carry",
				selected, t.module, c.ModuleSource, name)
		}
		for _, staged := range c.InitramfsModules {
			if staged == t.module {
				return nil
			}
		}
		return fmt.Errorf("socialweb-nodeimage: the %s VMM hands the guest its block device over "+
			"%s, and on this kernel that transport is a module rather than built in, so the "+
			"initramfs has to carry it: initramfs_modules names %v and not %s. Without it the "+
			"guest's PID 1 cannot load the transport before the root filesystem exists, so the "+
			"guest never sees /dev/vda and panics about a root device it never had",
			selected, t.module, c.InitramfsModules, t.module)
	}
	return nil
}

func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
