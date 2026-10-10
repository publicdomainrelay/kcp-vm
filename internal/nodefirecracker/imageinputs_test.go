//go:build linux

package nodefirecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type imageFixture struct {
	root       string
	configPath string
	kernel     string
	kernelEnv  string
	baseImage  string
	k3s        string
	agent      string
	agentPkg   string
	preinstall string
	modules    string
	cfg        BuildConfig
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func newImageFixture(t *testing.T) imageFixture {
	t.Helper()
	root := t.TempDir()
	f := imageFixture{
		root:       root,
		configPath: filepath.Join(root, "nodeimage.json"),
		kernel:     filepath.Join(root, "vmlinuz-6.16.4"),
		kernelEnv:  filepath.Join(root, "config-6.16.4"),
		baseImage:  filepath.Join(root, "base.squashfs"),
		k3s:        filepath.Join(root, "k3s"),
		agent:      filepath.Join(root, "kcpagent"),
		agentPkg:   filepath.Join(root, "socialweb-nodeagent"),
		modules:    filepath.Join(root, "modules"),
	}
	writeFileAt(t, f.kernel, "this host's kernel, as it was on the day of the build\n")
	writeFileAt(t, f.kernelEnv, "CONFIG_NETFILTER_XT_TARGET_NFLOG=m\n")
	writeFileAt(t, f.baseImage, "the base userspace, as it was on the day of the build\n")
	writeFileAt(t, f.k3s, "k3s version v1.31.1+k3s1\n")
	writeFileAt(t, f.agent, "the guest's agent, as it was on the day of the build\n")
	writeFileAt(t, filepath.Join(f.agentPkg, "main.go"), "package main\n")
	writeFileAt(t, filepath.Join(f.modules, "modules.dep"), "kernel/net/netfilter/xt_NFLOG.ko:\n")
	writeFileAt(t, filepath.Join(f.modules, "modules.builtin"), "kernel/net/netfilter/nf_conntrack.ko\n")
	writeFileAt(t, filepath.Join(f.modules, "kernel/net/netfilter/xt_NFLOG.ko"), "the extension\n")
	f.cfg = BuildConfig{
		Modules:          []string{"nf_conntrack", "xt_MASQUERADE", "xt_nat"},
		InitramfsModules: []string{"kernel/drivers/virtio/virtio_mmio"},
		Images:           []string{"rancher/mirrored-pause:3.10.2"},
		KernelSource:     f.kernel,
		KernelConfig:     f.kernelEnv,
		BaseImage:        f.baseImage,
		K3sBinary:        f.k3s,
		AgentBinary:      f.agent,
		AgentPackage:     f.agentPkg,
		ModuleSource:     f.modules,
	}
	f.writeConfig(t, f.cfg)
	return f
}

func (f imageFixture) writeConfig(t *testing.T, cfg BuildConfig) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encoding the builder's configuration: %v", err)
	}
	writeFileAt(t, f.configPath, string(raw))
}

func (f imageFixture) manifest(t *testing.T, cfg BuildConfig) Manifest {
	t.Helper()
	inputs, err := RecordInputs(f.configPath, cfg)
	if err != nil {
		t.Fatalf("recording the inputs the build read: %v", err)
	}
	seeded := make([]SeededImage, 0, len(cfg.Images))
	for _, reference := range cfg.Images {
		seeded = append(seeded, SeededImage{
			Reference:  reference,
			Repository: reference,
			Digest:     digestPrefix + "1111111111111111111111111111111111111111111111111111111111111111",
			Platform:   "linux/amd64",
		})
	}
	return Manifest{
		BootArgs:       []string{"console=ttyS0", "root=/dev/vda"},
		K3sBinary:      cfg.K3sBinary,
		K3sDataDir:     "/var/lib/rancher/k3s",
		GuestInterface: DefaultInterface,
		Modules:        cfg.Modules,
		Images:         seeded,
		Inputs:         &inputs,
	}
}

func (f imageFixture) writeImage(t *testing.T, manifest Manifest) {
	t.Helper()
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encoding the image manifest: %v", err)
	}
	writeFileAt(t, filepath.Join(f.root, ManifestName), string(raw))
}

func TestAnImageBuiltBeforeTheConfigurationGainedModulesIsRefused(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))

	rebuilt := f.cfg
	rebuilt.Modules = append(append([]string(nil), f.cfg.Modules...), "xt_NFLOG", "xt_physdev")
	f.writeConfig(t, rebuilt)

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("an image built before the configuration gained xt_NFLOG and xt_physdev was " +
			"accepted, and a guest booted from it would run a kernel missing both extensions: " +
			"iptables-restore inside the guest stops at the first extension it cannot find and " +
			"every rule after it is absent, which is a cluster with no firewall and an apiserver " +
			"with no address, and nothing in the cluster says which image caused it")
	}
	for _, want := range []string{"xt_NFLOG", "xt_physdev", "3", "5", f.configPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it does not say what differs between the "+
				"image and the configuration in front of it: %v", want, err)
		}
	}
}

func TestAnImageBuiltFromTheConfigurationInFrontOfItLoads(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := manifest.CheckInputs(); err != nil {
		t.Fatalf("an image built from the configuration in front of it was refused: %v", err)
	}

	reordered := f.cfg
	reordered.Modules = []string{"xt_nat", "xt_MASQUERADE", "nf_conntrack"}
	f.writeConfig(t, reordered)
	if err := manifest.CheckInputs(); err != nil {
		t.Fatalf("the module list is a SET and the configuration names the same modules in "+
			"another order, so a refusal here would send the operator to rebuild an image that is "+
			"already the one the configuration describes: %v", err)
	}
}

func TestAnImageWhoseK3sBinaryWasReplacedIsRefused(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))
	writeFileAt(t, f.k3s, "k3s version v1.32.0+k3s1\n")

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("the k3s binary this image was built from was replaced and the image was " +
			"accepted: a k3s bump moves the add-on list and the seeded image digests together, so " +
			"a node booted from the old binary runs the old cluster's system pods, and both " +
			"clusters are internally consistent so nothing reports it")
	}
	for _, want := range []string{f.k3s, digestPrefix, "k3s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestAnImageWhoseBaseImageWasReplacedIsRefused(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))
	writeFileAt(t, f.baseImage, "a later base userspace, which the image does not carry\n")

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("the base image was replaced and the image built from the old one was accepted")
	}
	if !strings.Contains(err.Error(), f.baseImage) {
		t.Errorf("the refusal does not name the base image it cannot match: %v", err)
	}
}

func TestAnImageBuiltFromASymlinkedPathIsTheImageTheDirectPathDescribes(t *testing.T) {
	f := newImageFixture(t)
	link := filepath.Join(f.root, "the-host-as-this-configuration-reaches-it")
	if err := os.Symlink(f.root, link); err != nil {
		t.Fatalf("linking %s to %s: %v", link, f.root, err)
	}
	throughLink := f.cfg
	throughLink.KernelSource = filepath.Join(link, filepath.Base(f.kernel))
	throughLink.KernelConfig = filepath.Join(link, filepath.Base(f.kernelEnv))
	throughLink.BaseImage = filepath.Join(link, filepath.Base(f.baseImage))
	throughLink.K3sBinary = filepath.Join(link, filepath.Base(f.k3s))

	inputs, err := RecordInputs(f.configPath, throughLink)
	if err != nil {
		t.Fatalf("recording what a build through the symlink read: %v", err)
	}
	f.writeImage(t, Manifest{
		BootArgs:       []string{"console=ttyS0", "root=/dev/vda"},
		K3sBinary:      throughLink.K3sBinary,
		K3sDataDir:     "/var/lib/rancher/k3s",
		GuestInterface: DefaultInterface,
		Modules:        throughLink.Modules,
		Images: []SeededImage{{
			Reference: throughLink.Images[0],
			Digest:    digestPrefix + strings.Repeat("1", 64),
			Platform:  "linux/amd64",
		}},
		Inputs: &inputs,
	})

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	f.writeConfig(t, f.cfg)
	if err := manifest.Validate(); err != nil {
		t.Fatalf("the image recorded through a symlinked path is refused before it is compared to "+
			"anything: %v", err)
	}
	if err := manifest.CheckInputs(); err != nil {
		t.Fatalf("an image built from a host reached through a symlink was refused when the same "+
			"files are named directly. The record holds the path the build read and the check holds "+
			"the path the configuration names, so a check that compared the two as written would "+
			"refuse every image that build produced and send the operator to rebuild 2.3 GB of "+
			"inputs that did not change: %v", err)
	}
}

func TestAnImageThatNamesADifferentKernelIsRefused(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))

	other := filepath.Join(f.root, "vmlinuz-6.17.0")
	writeFileAt(t, other, "a kernel the image was never built from\n")
	moved := f.cfg
	moved.KernelSource = other
	f.writeConfig(t, moved)

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("a configuration naming a kernel other than the one the image carries was accepted")
	}
	for _, want := range []string{other, f.kernel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it does not say which two kernels it is "+
				"comparing: %v", want, err)
		}
	}
}

func TestAnImageThatSeedsFewerImagesThanTheConfigurationNamesIsRefused(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))

	rebuilt := f.cfg
	rebuilt.Images = []string{"rancher/mirrored-pause:3.10.2", "rancher/mirrored-coredns-coredns:1.14.6"}
	f.writeConfig(t, rebuilt)

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("an image that seeds one image while the configuration names two was accepted: " +
			"nothing in the guest reaches a registry, so a pod asking for the missing one does not " +
			"fail, it waits")
	}
	if !strings.Contains(err.Error(), "rancher/mirrored-coredns-coredns:1.14.6") {
		t.Errorf("the refusal does not name the image that nothing seeded: %v", err)
	}
}

func TestAnImageThatRecordsNoInputsIsRefused(t *testing.T) {
	f := newImageFixture(t)
	manifest := f.manifest(t, f.cfg)
	manifest.Inputs = nil

	err := manifest.Validate()
	if err == nil {
		t.Fatal("a manifest that records nothing about what it was built from was accepted: an " +
			"unanswerable provenance is what this whole check exists to refuse, and it is the " +
			"state every image built before this record existed is in")
	}
	if !strings.Contains(err.Error(), "records nothing about the inputs") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
	if checkErr := manifest.CheckInputs(); checkErr == nil {
		t.Fatal("CheckInputs accepted a manifest with no recorded inputs")
	}
}

func TestAnImageWhoseRecordedConfigurationIsGoneIsRefused(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))
	if err := os.Remove(f.configPath); err != nil {
		t.Fatalf("removing the configuration: %v", err)
	}

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("the configuration the image says it was built from is gone and the image was " +
			"accepted: nothing on this host can now show what the image was built from")
	}
	if !strings.Contains(err.Error(), f.configPath) {
		t.Errorf("the refusal does not name the configuration it could not read: %v", err)
	}
}

func TestAnImageWhoseRecordedConfigurationNamesNoModulesIsRefusedByKey(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))
	writeFileAt(t, f.configPath, `{"kernel_source":`+fmt.Sprintf("%q", f.kernel)+
		`,"kernel_config":`+fmt.Sprintf("%q", f.kernelEnv)+
		`,"base_image":`+fmt.Sprintf("%q", f.baseImage)+
		`,"k3s_binary":`+fmt.Sprintf("%q", f.k3s)+
		`,"images":["rancher/mirrored-pause:3.10.2"],`+
		`"initramfs_modules":["kernel/drivers/virtio/virtio_mmio"]}`)

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("a configuration with no module key at all was accepted, and comparing an " +
			"absent list against the image's is how a renamed key reads as an image that needs " +
			"rebuilding")
	}
	if !strings.Contains(err.Error(), "modules") {
		t.Errorf("the refusal does not name the key it could not read: %v", err)
	}
}

func moduleRootOfTheTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod at or above %s, so this test cannot say where the module is", dir)
		}
		dir = parent
	}
}

func TestAnAgentPackageIsRelativeToTheModuleAndNotToWhereTheReaderStands(t *testing.T) {
	f := newImageFixture(t)
	cfg := f.cfg
	cfg.AgentPackage = "./cmd/socialweb-nodeagent"

	fromThePackage, err := Fingerprint(f.configPath, cfg)
	if err != nil {
		t.Fatalf("naming the inputs from the directory the configuration was read in: %v", err)
	}

	t.Chdir(moduleRootOfTheTest(t))
	fromTheRoot, err := Fingerprint(f.configPath, cfg)
	if err != nil {
		t.Fatalf("naming the inputs from the module's own root: %v", err)
	}
	t.Chdir(filepath.Join(moduleRootOfTheTest(t), "internal"))
	fromBelow, err := Fingerprint(f.configPath, cfg)
	if err != nil {
		t.Fatalf("naming the inputs from a directory below the module's root: %v", err)
	}
	if fromTheRoot != fromThePackage || fromBelow != fromThePackage {
		t.Fatalf("one configuration names %s from the directory it was read in, %s from the "+
			"module's root and %s from below it. The package path is written relative to the module, "+
			"which is how go resolves it, so a reader who is one directory deeper asks the store for "+
			"a name no build has taken and the answer is a rebuild of 2.3 GB of unchanged inputs",
			fromThePackage, fromTheRoot, fromBelow)
	}

	t.Chdir(t.TempDir())
	if _, err := Fingerprint(f.configPath, cfg); err == nil {
		t.Fatal("a package path was resolved from a directory that is inside no module at all, and " +
			"there is nothing there for a relative path to be relative to: whatever it picked would " +
			"be recorded as the source the agent in the image was built from")
	} else if !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("the refusal does not name the file it looked for and did not find: %v", err)
	}
}

func (f imageFixture) withPreinstall(t *testing.T) imageFixture {
	t.Helper()
	f.preinstall = filepath.Join(f.root, "preinstall.json")
	writeFileAt(t, f.preinstall, `{"packages":["cloud-init","jq"],"files":[],"runs":[]}`)
	f.cfg.Preinstall = f.preinstall
	f.writeConfig(t, f.cfg)
	return f
}

func TestAnImageBuiltWithoutTheManifestTheConfigurationNamesIsRefused(t *testing.T) {
	f := newImageFixture(t).withPreinstall(t)
	f.writeImage(t, f.manifest(t, f.cfg))

	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := manifest.CheckInputs(); err != nil {
		t.Fatalf("an image built from the configuration in front of it was refused: %v", err)
	}

	without := f.cfg
	without.Preinstall = ""
	f.writeConfig(t, without)
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("a configuration that stopped naming the manifest its image was built with was " +
			"accepted, and the image it describes carries software chosen by a list nothing names " +
			"any more")
	}
	if !strings.Contains(err.Error(), "preinstall manifest") {
		t.Errorf("the refusal does not name the input that cannot be compared: %v", err)
	}

	f.writeConfig(t, f.cfg)
	if err := manifest.CheckInputs(); err != nil {
		t.Fatalf("the configuration the image was built from was refused once it was written back: %v", err)
	}
	writeFileAt(t, f.preinstall, `{"packages":["cloud-init","jq"],"files":[],"runs":["cloud-init --version"]}`)
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("a manifest whose contents changed was accepted, so the image is the one built from " +
			"a list of packages that is not the list in front of it")
	}
	if !strings.Contains(err.Error(), "preinstall manifest") {
		t.Errorf("the refusal does not name the manifest whose contents moved: %v", err)
	}
}

func TestAManifestTheImageWasNotBuiltFromIsRefused(t *testing.T) {
	f := newImageFixture(t)
	f.writeImage(t, f.manifest(t, f.cfg))

	withOne := f.withPreinstall(t)
	if withOne.cfg.Preinstall == "" {
		t.Fatal("the fixture names no manifest, so this test compared nothing")
	}
	manifest, err := LoadManifest(f.root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	err = manifest.CheckInputs()
	if err == nil {
		t.Fatal("an image built with no manifest at all was accepted by a configuration that names " +
			"one, so the guest installs at first boot what the image was meant to have baked")
	}
	if !strings.Contains(err.Error(), "preinstall manifest") {
		t.Errorf("the refusal does not name the input that differs: %v", err)
	}
}

func TestTheHostBusyboxIsRecordedAndAnImageBuiltWithAnotherOneIsRefused(t *testing.T) {
	f := newImageFixture(t)
	manifest := f.manifest(t, f.cfg)
	if manifest.Inputs.Busybox != HostBusybox || manifest.Inputs.BusyboxDigest == "" {
		t.Fatalf("a build records carrying this host's busybox as %q %q. The initramfs carries that "+
			"binary as the insmod the guest's PID 1 loads its transport module with, so a build that "+
			"does not record which one it copied is an image whose contents were decided by a file "+
			"outside every input a configuration names",
			manifest.Inputs.Busybox, manifest.Inputs.BusyboxDigest)
	}
	if err := CompareImageToConfig(manifest, f.configPath, f.cfg); err != nil {
		t.Fatalf("an image built from the configuration in front of it was refused: %v", err)
	}

	manifest.Inputs.BusyboxDigest = digestPrefix + strings.Repeat("0", 64)
	err := CompareImageToConfig(manifest, f.configPath, f.cfg)
	if err == nil {
		t.Fatal("an image whose initramfs carries a different busybox was accepted, so a host that " +
			"upgraded busybox boots an image that loads a module through a binary the record cannot " +
			"identify")
	}
	if !strings.Contains(err.Error(), "busybox") {
		t.Errorf("the refusal does not name the input that differs: %v", err)
	}
}
