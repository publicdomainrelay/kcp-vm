package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func TestEveryInputTheBuildReadsMovesTheNameAndTheComparison(t *testing.T) {
	for _, mutation := range []struct {
		name   string
		mutate func(t *testing.T, cfg *Config)
	}{
		{"the kernel", func(t *testing.T, cfg *Config) {
			writeFileAt(t, cfg.KernelSource, "another kernel\n")
		}},
		{"the kernel configuration", func(t *testing.T, cfg *Config) {
			writeFileAt(t, cfg.KernelConfig, "CONFIG_OTHER=y\n")
		}},
		{"the base image", func(t *testing.T, cfg *Config) {
			writeFileAt(t, cfg.BaseImage, "another base userspace\n")
		}},
		{"the k3s binary", func(t *testing.T, cfg *Config) {
			writeFileAt(t, cfg.K3sBinary, "k3s version v1.32.0+k3s1\n")
		}},
		{"the guest init", func(t *testing.T, cfg *Config) {
			writeFileAt(t, cfg.InitBinary, "another init\n")
		}},
		{"a module file under the tree the build copies", func(t *testing.T, cfg *Config) {
			writeFileAt(t, filepath.Join(cfg.ModuleSource, "kernel/net/netfilter/nf_conntrack.ko"),
				"another connection tracker\n")
		}},
		{"a module the configuration names", func(t *testing.T, cfg *Config) {
			cfg.Modules = append(append([]string(nil), cfg.Modules...), "xt_physdev")
		}},
		{"an initramfs module the configuration names", func(t *testing.T, cfg *Config) {
			cfg.InitramfsModules = append(append([]string(nil), cfg.InitramfsModules...), "kernel/drivers/net/virtio_net")
		}},
		{"a seeded image", func(t *testing.T, cfg *Config) {
			cfg.Images = append(append([]string(nil), cfg.Images...), "rancher/mirrored-coredns-coredns:1.14.6")
		}},
		{"the boot arguments", func(t *testing.T, cfg *Config) {
			cfg.BootArgs = append(append([]string(nil), cfg.BootArgs...), "selinux=0")
		}},
		{"the guest's k3s data directory", func(t *testing.T, cfg *Config) {
			cfg.K3sDataDir = "/var/lib/rancher/k3s-elsewhere"
		}},
		{"the rootfs label", func(t *testing.T, cfg *Config) {
			cfg.RootfsLabel = "anotherlabel"
		}},
		{"the rootfs headroom", func(t *testing.T, cfg *Config) {
			cfg.RootfsHeadroomMiB = cfg.RootfsHeadroomMiB + 1
		}},
		{"the init's package", func(t *testing.T, cfg *Config) {
			elsewhere := filepath.Join(t.TempDir(), "socialweb-nodeinit-elsewhere")
			if err := os.MkdirAll(elsewhere, 0o755); err != nil {
				t.Fatalf("creating %s: %v", elsewhere, err)
			}
			writeFileAt(t, filepath.Join(elsewhere, "main.go"), "package main\n")
			cfg.InitPackage = elsewhere
		}},
		{"the init's build flags", func(t *testing.T, cfg *Config) {
			cfg.InitGoFlags = append(append([]string(nil), cfg.InitGoFlags...), "-race")
		}},
		{"the VMM", func(t *testing.T, cfg *Config) {
			cfg.VMM = "qemu"
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			before := namedConfig(t)
			beforeName := nameOf(t, before)

			after := namedConfig(t)
			mutation.mutate(t, &after)
			writeConfigFile(t, after)

			recorded, err := nodefirecracker.RecordInputs(after.Path, buildConfigOf(after))
			if err != nil {
				t.Fatalf("recording the inputs of the changed configuration: %v", err)
			}
			manifest := nodefirecracker.Manifest{
				Modules:   before.Modules,
				K3sBinary: before.K3sBinary,
				Inputs:    &recorded,
			}
			changedName, err := nodefirecracker.Fingerprint(after.Path, buildConfigOf(after))
			if err != nil {
				t.Fatalf("naming the changed configuration: %v", err)
			}
			if changedName == beforeName {
				t.Fatalf("changing %s left the image's name at %s, so a build would answer `up to "+
					"date` and the guest would run the previous answer", mutation.name, beforeName)
			}
			earlier, err := nodefirecracker.RecordInputs(before.Path, buildConfigOf(before))
			if err != nil {
				t.Fatalf("recording the inputs of the earlier configuration: %v", err)
			}
			manifest.Inputs = &earlier
			if err := nodefirecracker.CompareImageToConfig(manifest, after.Path, buildConfigOf(after)); err == nil {
				t.Fatalf("a boot over an image built before %s changed was accepted: the two things "+
					"this test holds together are the NAME a build is given and the COMPARISON a boot "+
					"makes, and a change that moves one and not the other is a change one of them "+
					"cannot see", mutation.name)
			}
		})
	}
}

func TestAnImageBuiltBeforeTheRecordCoveredTheInitIsRefused(t *testing.T) {
	cfg := namedConfig(t)
	inputs, err := nodefirecracker.RecordInputs(cfg.Path, buildConfigOf(cfg))
	if err != nil {
		t.Fatalf("recording the inputs: %v", err)
	}
	inputs.InitBinary = ""
	inputs.InitBinaryDigest = ""
	inputs.ModuleSource = ""
	inputs.ModuleSourceDigest = ""
	manifest := nodefirecracker.Manifest{
		Modules:   cfg.Modules,
		K3sBinary: cfg.K3sBinary,
		Inputs:    &inputs,
	}
	err = nodefirecracker.CompareImageToConfig(manifest, cfg.Path, buildConfigOf(cfg))
	if err == nil {
		t.Fatal("an image whose record says nothing about the init it carries or the module tree it " +
			"copied was accepted, and that is the shape every image built before this change has: the " +
			"record cannot show they match, so booting one is booting an init nobody can account for")
	}
	for _, want := range []string{"guest init", "tree"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it does not say which input the record cannot "+
				"show: %v", want, err)
		}
	}
}

func namedConfig(t *testing.T) Config {
	t.Helper()
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	return cfg
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
