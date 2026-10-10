//go:build linux

package nodefirecracker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func kernelTree(t *testing.T, root, release string) string {
	t.Helper()
	dir := filepath.Join(root, release)
	writeFileAt(t, filepath.Join(dir, kernelSourceFile), "the kernel this host boots\n")
	writeFileAt(t, filepath.Join(dir, kernelConfigFile), "CONFIG_NETFILTER_XT_TARGET_NFLOG=m\n")
	return dir
}

func fixedResolver(root, release string) KernelResolver {
	return KernelResolver{ModulesRoot: root, Release: func() (string, error) { return release, nil }}
}

func resolvedOrFail(t *testing.T, path string) string {
	t.Helper()
	resolved, err := resolvePath(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	return resolved
}

func TestDiscoveringKernelResolvesTheRunningOne(t *testing.T) {
	root := t.TempDir()
	running := kernelTree(t, root, "7.2.8-200.fc44.x86_64")
	kernelTree(t, root, "7.1.0-200.fc44.x86_64")

	got, err := fixedResolver(root, "7.2.8-200.fc44.x86_64").Resolve(KernelDiscover, KernelDiscover, KernelDiscover)
	if err != nil {
		t.Fatalf("resolving the kernel this host is running: %v", err)
	}
	if got.Origin != KernelHostRunning {
		t.Errorf("the running kernel's tree was resolved as %q and not %q, so a host that is running "+
			"one kernel has a configuration that reads another", got.Origin, KernelHostRunning)
	}
	if got.Version != "7.2.8-200.fc44.x86_64" {
		t.Errorf("the resolved kernel is %q and not the release the host reported", got.Version)
	}
	if want := resolvedOrFail(t, filepath.Join(running, kernelSourceFile)); got.Source != want {
		t.Errorf("the kernel source resolved to %s and not to the running kernel's own %s", got.Source, want)
	}
	if want := resolvedOrFail(t, running); got.Modules != want {
		t.Errorf("the module tree resolved to %s and not to the running kernel's own %s", got.Modules, want)
	}
}

func TestDiscoveringKernelFallsBackToTheNewestInstalled(t *testing.T) {
	root := t.TempDir()
	older := kernelTree(t, root, "7.2.6-200.fc44.x86_64")
	newer := kernelTree(t, root, "7.2.9-200.fc44.x86_64")
	incomplete := filepath.Join(root, "7.3.0-200.fc44.x86_64")
	writeFileAt(t, filepath.Join(incomplete, kernelSourceFile), "a kernel with no configuration\n")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(older, kernelSourceFile), past, past); err != nil {
		t.Fatalf("aging %s: %v", older, err)
	}
	if err := os.Chtimes(filepath.Join(newer, kernelSourceFile), time.Now(), time.Now()); err != nil {
		t.Fatalf("dating %s: %v", newer, err)
	}

	got, err := fixedResolver(root, "7.2.8-200.fc44.x86_64").Resolve(KernelDiscover, KernelDiscover, KernelDiscover)
	if err != nil {
		t.Fatalf("resolving a kernel when the running one is absent: %v", err)
	}
	if got.Origin != KernelNewestInstalled {
		t.Errorf("a host whose running kernel is not installed resolved %q and not %q, so the "+
			"fallback is not what the log will name", got.Origin, KernelNewestInstalled)
	}
	if got.Version != "7.2.9-200.fc44.x86_64" {
		t.Errorf("the fallback resolved %q and not the newest installed kernel, and a tree with no "+
			"configuration was chosen over one with a configuration", got.Version)
	}
}

func TestANamedKernelIsNotDiscovered(t *testing.T) {
	got, err := fixedResolver(t.TempDir(), "7.2.8-200.fc44.x86_64").
		Resolve("/boot/vmlinuz-6.16.4", "/boot/config-6.16.4", "/lib/modules/6.16.4")
	if err != nil {
		t.Fatalf("resolving a kernel the configuration names: %v", err)
	}
	if got.Origin != KernelNamed {
		t.Errorf("a configuration naming its kernel was resolved as %q, and a name that is read "+
			"would move the image's name when the host's kernel moves", got.Origin)
	}
	if got.Source != "/boot/vmlinuz-6.16.4" || got.Config != "/boot/config-6.16.4" ||
		got.Modules != "/lib/modules/6.16.4" {
		t.Errorf("a named kernel was rewritten to source %s config %s modules %s",
			got.Source, got.Config, got.Modules)
	}
	if got.Version != "" {
		t.Errorf("a named kernel reported the discovered version %q", got.Version)
	}
}

func TestAMixedKernelSelectionIsRefused(t *testing.T) {
	if _, err := fixedResolver(t.TempDir(), "7.2.8-200.fc44.x86_64").
		Resolve(KernelDiscover, "/boot/config-6.16.4", "/lib/modules/6.16.4"); err == nil {
		t.Fatal("a configuration that discovers its kernel and names its module tree was accepted, " +
			"so the guest's kernel and the tree modprobe loads against it are chosen from two places " +
			"and nothing compares them")
	}
}

func TestAConfigurationThatDiscoversItsKernelIsResolvedBeforeItIsRecorded(t *testing.T) {
	f := newImageFixture(t)
	f.cfg.KernelSource = KernelDiscover
	f.cfg.KernelConfig = KernelDiscover
	f.cfg.ModuleSource = KernelDiscover
	f.writeConfig(t, f.cfg)

	root := t.TempDir()
	release := "7.2.8-200.fc44.x86_64"
	dir := kernelTree(t, root, release)
	loaded, err := loadBuildConfig(f.configPath, fixedResolver(root, release))
	if err != nil {
		t.Fatalf("loading a configuration that asks for the host's kernel: %v", err)
	}
	if want := resolvedOrFail(t, filepath.Join(dir, kernelSourceFile)); loaded.KernelSource != want {
		t.Errorf("the loaded configuration carries kernel_source %s and not the discovered %s, so "+
			"the name a build is given and the record it writes would hold %q rather than the file "+
			"the build actually read", loaded.KernelSource, want, KernelDiscover)
	}
	if want := resolvedOrFail(t, filepath.Join(dir, kernelConfigFile)); loaded.KernelConfig != want {
		t.Errorf("the loaded configuration carries kernel_config %s and not the discovered %s",
			loaded.KernelConfig, want)
	}
	if want := resolvedOrFail(t, dir); loaded.ModuleSource != want {
		t.Errorf("the loaded configuration carries module_source %s and not the discovered %s",
			loaded.ModuleSource, want)
	}
}
