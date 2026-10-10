package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func imageFixture(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	host := filepath.Join(root, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatalf("creating %s: %v", host, err)
	}
	for name, body := range map[string]string{
		"vmlinuz-6.16.4": "this host's kernel, as it was on the day of the build\n",
		"config-6.16.4":  "CONFIG_NETFILTER_XT_TARGET_NFLOG=m\n",
		"base.squashfs":  "the base userspace, as it was on the day of the build\n",
		"k3s":            "k3s version v1.31.1+k3s1\n",
	} {
		if err := os.WriteFile(filepath.Join(host, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	cfg := validConfig(t)
	cfg.Path = filepath.Join(root, "nodeimage.json")
	cfg.OutDir = filepath.Join(root, "store")
	cfg.KernelSource = filepath.Join(host, "vmlinuz-6.16.4")
	cfg.KernelConfig = filepath.Join(host, "config-6.16.4")
	cfg.BaseImage = filepath.Join(host, "base.squashfs")
	cfg.K3sBinary = filepath.Join(host, "k3s")
	cfg.Modules = []string{"nf_conntrack", "xt_MASQUERADE", "xt_nat"}
	cfg.InitramfsModules = []string{"kernel/drivers/virtio/virtio_mmio"}
	cfg.Images = []string{"rancher/mirrored-pause:3.10.2"}
	return cfg
}

func writeConfigFile(t *testing.T, cfg Config) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encoding the builder's configuration: %v", err)
	}
	if err := os.WriteFile(cfg.Path, raw, 0o600); err != nil {
		t.Fatalf("writing %s: %v", cfg.Path, err)
	}
}

func nameOf(t *testing.T, cfg Config) string {
	t.Helper()
	fingerprint, err := nodefirecracker.Fingerprint(cfg.Path, buildConfigOf(cfg))
	if err != nil {
		if errors.Is(err, ErrRegistryUnreachable) {
			livegate.Require(t, "the registry an image reference names could not be reached, so the "+
				"name these inputs hash to cannot be computed and this test cannot say anything "+
				"about it: %v", err)
			return ""
		}
		t.Fatalf("naming the inputs this configuration reads: %v", err)
	}
	return fingerprint
}

func writeImageUnderItsName(t *testing.T, cfg Config, fingerprint string) string {
	t.Helper()
	dir := storeDir(cfg, fingerprint)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	for _, name := range []string{
		nodefirecracker.VMLinuxName,
		nodefirecracker.InitramfsName,
		nodefirecracker.RootfsImageName,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("a whole build put this here\n"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	written := cfg
	written.OutDir = dir
	tars := []imageTar{{
		reference:  cfg.Images[0],
		repository: cfg.Images[0],
		digest:     "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		platform:   "linux/amd64",
		guestName:  imageTarName(cfg.Images[0]),
		bytes:      4096,
	}}
	if err := writeManifest(written, Artifacts{}, tars); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	return dir
}

func entriesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAnImageUnderTheNameItsInputsGiveItIsNotBuiltAgain(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	dir := writeImageUnderItsName(t, cfg, nameOf(t, cfg))

	if err := Main(cfg.Path); err != nil {
		t.Fatalf("a second run over a configuration whose image is already in the store did not "+
			"leave it alone: %v -- the run is meant to be unconditional, so a run that builds, "+
			"writes or pulls anything while the image it names is already there is a run nobody can "+
			"put on a timer, and the build would have failed here (the module tree this fixture "+
			"names does not exist), so what came back instead was not a build", err)
	}
	if names := entriesIn(t, cfg.OutDir); len(names) != 1 || names[0] != nameOf(t, cfg) {
		t.Fatalf("a run that found the image already built left %q in the store, and the store is "+
			"one directory per name with nothing beside it", names)
	}
	if _, err := os.Stat(filepath.Join(dir, nodefirecracker.ManifestName)); err != nil {
		t.Fatalf("the run that reported the image up to date did not leave it whole: %v", err)
	}
}

func TestAConfigurationThatChangedIsNeverGivenTheImageBuiltBeforeIt(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	before := nameOf(t, cfg)
	old := writeImageUnderItsName(t, cfg, before)

	changed := cfg
	changed.Modules = append(append([]string(nil), cfg.Modules...), "xt_NFLOG")
	writeConfigFile(t, changed)
	after := nameOf(t, changed)

	if after == before {
		t.Fatal("a configuration that gained a kernel module asks for the image built without it, " +
			"so the store hands out a guest whose iptables-restore stops at the first extension the " +
			"image does not carry and whose kube-proxy leaves 10.43.0.1:443 without an address, and " +
			"nothing says which image caused it")
	}
	err := Main(changed.Path)
	if err == nil {
		t.Fatal("a run over a changed configuration returned cleanly without building, so it " +
			"treated the image built from the older configuration as the one this configuration " +
			"names")
	}
	if strings.Contains(err.Error(), "up to date") {
		t.Fatalf("the run compared the changed configuration against the image built from the older "+
			"one instead of asking for the name the changed inputs give: %v", err)
	}
	if present, checkErr := completeImage(changed, before); checkErr != nil || !present {
		t.Fatalf("the image built from the older configuration was disturbed by a run over the "+
			"newer one (%v, %v), and nothing about it changed: it keeps its own name and nothing "+
			"compares or rejects it", checkErr, present)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("the image built from the older configuration is gone: %v", err)
	}
}

func TestABuildThatDoesNotFinishLeavesNoNameToAskFor(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	fingerprint := nameOf(t, cfg)

	err := Main(cfg.Path)
	if err == nil {
		t.Fatal("a build whose module tree is absent returned cleanly")
	}
	if _, statErr := os.Stat(storeDir(cfg, fingerprint)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("%s exists after a build that did not finish (%v): a name that exists for an image "+
			"that was never completed is an image a substrate can ask for and a guest can be booted "+
			"from, which is the whole reason the build is renamed into place rather than written "+
			"into it", storeDir(cfg, fingerprint), statErr)
	}
	if names := entriesIn(t, cfg.OutDir); len(names) != 0 {
		t.Fatalf("a build that did not finish left %q in the store, so a partial build has a name "+
			"after all", names)
	}
}

func TestTheImageUnderItsNameIsTheOneTheSubstrateBoots(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	dir := writeImageUnderItsName(t, cfg, nameOf(t, cfg))

	manifest, err := nodefirecracker.LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("the image the store names for this configuration is refused by the substrate "+
			"before it is compared to anything: %v", err)
	}
	if err := manifest.CheckInputs(); err != nil {
		t.Fatalf("the image the store names for this configuration is refused by the substrate's own "+
			"provenance check, so the name the builder computed and the record the substrate reads "+
			"disagree about the same inputs: %v", err)
	}
	if err := imageInDir(dir).Validate(); err != nil {
		t.Fatalf("the image the store names is missing one of the three files the guest boots "+
			"from: %v", err)
	}
}

func TestAnImageWhoseNameItsRecordDoesNotHashToIsRefusedInBothDirections(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	dir := writeImageUnderItsName(t, cfg, nameOf(t, cfg))

	manifest, err := nodefirecracker.LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	manifest.Inputs.K3sBinary = filepath.Join(cfg.OutDir, "a-k3s-no-build-read")
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encoding the manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, nodefirecracker.ManifestName), raw, 0o600); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}

	err = Main(cfg.Path)
	if err == nil {
		t.Fatal("a directory named by one set of inputs and holding the record of another was " +
			"consumed as the image this configuration names")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("the refusal does not name the directory it is about: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, nodefirecracker.ManifestName)); statErr != nil {
		t.Fatalf("the refusal removed or replaced the directory it refused: %v", statErr)
	}
}

func TestAnImageThatRecordsNothingAboutItsInputsIsNotTheOneAskedFor(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	fingerprint := nameOf(t, cfg)

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", cfg.OutDir, err)
	}
	legacy := nodefirecracker.Manifest{
		BootArgs:       []string{"console=ttyS0", "root=/dev/vda"},
		Vcpu:           2,
		MemMiB:         2048,
		GuestInterface: nodefirecracker.DefaultInterface,
		K3sBinary:      cfg.K3sBinary,
		K3sDataDir:     cfg.K3sDataDir,
		Modules:        cfg.Modules,
	}
	raw, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("encoding the manifest: %v", err)
	}
	legacyDir := filepath.Join(cfg.OutDir, "image")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", legacyDir, err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, nodefirecracker.ManifestName), raw, 0o600); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}

	present, err := completeImage(cfg, fingerprint)
	if err != nil {
		t.Fatalf("looking for the image this configuration names: %v", err)
	}
	if present {
		t.Fatal("an image that records nothing about the inputs it was built from was taken as the " +
			"image this configuration names: an absent record proves nothing, and the image built " +
			"before a configuration gained kernel modules looks exactly like one built after")
	}
	if err := Main(cfg.Path); err == nil {
		t.Fatal("a run over a store whose only image records nothing returned cleanly without " +
			"building")
	}
	if _, err := os.Stat(filepath.Join(legacyDir, nodefirecracker.ManifestName)); err != nil {
		t.Fatalf("the image built before the store existed was removed or replaced: %v", err)
	}
}

func TestAStagingDirectoryNamesTheProcessThatIsWritingIt(t *testing.T) {
	fingerprint := nodefirecracker.FingerprintPrefix + strings.Repeat("a", 64)
	name := strings.Replace(stagingPattern(fingerprint), "*", "1234567890", 1)

	got, pid, ok := stagingOf(name)
	if !ok {
		t.Fatalf("%s was not recognised as a build in progress, and nothing else in the store reaps "+
			"the directory a killed build leaves behind: a build is named by the same fingerprint "+
			"it will be published under, and the process writing it has to be in that name, or the "+
			"only way to tell a build that is running from one that died is to guess at its age",
			name)
	}
	if got != fingerprint {
		t.Fatalf("the staging directory of %s reads back as %s, so a reap would count it against "+
			"the wrong image", fingerprint, got)
	}
	if pid != os.Getpid() {
		t.Fatalf("the staging directory names pid %d and this process is %d", pid, os.Getpid())
	}
	if !processAlive(pid) {
		t.Fatalf("this process's own pid, %d, is reported as gone", pid)
	}

	for _, other := range []string{
		fingerprint,
		"image",
		".building-1-2",
		"." + fingerprint + stagingName + "1234567890",
		"." + fingerprint + stagingName + "-0-1234567890",
		".sha256-abc" + stagingName + "-1-1234567890",
		"." + fingerprint + stagingName + "-notanumber-1",
	} {
		if _, _, ok := stagingOf(other); ok {
			t.Errorf("%s was taken for a build in progress. The directories this reaps are the ones "+
				"this builder names, and anything else in the store is someone else's: a reap that "+
				"recognised a name it did not write is one that removes what it does not own", other)
		}
	}
}

func TestAnInputThatCannotBeReadIsNotAnInputThatHasNotChanged(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	writeImageUnderItsName(t, cfg, nameOf(t, cfg))
	if err := os.Remove(cfg.BaseImage); err != nil {
		t.Fatalf("removing the base image: %v", err)
	}

	err := Main(cfg.Path)
	if err == nil {
		t.Fatal("a run whose base image cannot be read returned cleanly, so it reported the image " +
			"as the one this configuration names on the strength of an input it never read")
	}
	if !strings.Contains(err.Error(), "cannot tell") {
		t.Fatalf("the refusal does not say that it cannot tell whether the image is the one the "+
			"configuration names: %v", err)
	}
	if !strings.Contains(err.Error(), cfg.BaseImage) {
		t.Fatalf("the refusal does not name the input it could not read: %v", err)
	}
}
