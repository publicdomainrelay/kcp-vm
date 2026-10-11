package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

type fieldRole string

const (
	readByTheBuild fieldRole = "read"

	writtenByTheBuild fieldRole = "written"

	usedElsewhere fieldRole = "elsewhere"
)

type fieldDeclaration struct {
	Role fieldRole

	Covers []string
}

const configFileField = "(the configuration file the builder is told to read)"

const hostBusyboxField = "(this host's busybox, which the initramfs carries)"

const builderExecutableField = "(the builder that reads the configuration, whose own binary decides what it writes)"

var fieldDeclarations = map[string]fieldDeclaration{
	configFileField:        {Role: readByTheBuild, Covers: []string{"configPath"}},
	hostBusyboxField:       {Role: readByTheBuild, Covers: []string{"busybox", "busyboxDigest"}},
	builderExecutableField: {Role: readByTheBuild, Covers: []string{"builderDigest"}},
	"vmm":                  {Role: readByTheBuild, Covers: []string{"vmm"}},
	"firecracker":          {Role: usedElsewhere},
	"qemu":                 {Role: usedElsewhere},
	"kernel_source":        {Role: readByTheBuild, Covers: []string{"kernelSource", "kernelSourceDigest"}},
	"kernel_config":        {Role: readByTheBuild, Covers: []string{"kernelConfig", "kernelConfigDigest"}},
	"module_source":        {Role: readByTheBuild, Covers: []string{"moduleSource", "moduleSourceDigest"}},
	"base_image":           {Role: readByTheBuild, Covers: []string{"baseImage", "baseImageDigest"}},
	"stage_dir":            {Role: writtenByTheBuild},
	"out_dir":              {Role: writtenByTheBuild},
	"image_cache_dir":      {Role: writtenByTheBuild},
	"init_package":         {Role: readByTheBuild, Covers: []string{"initPackage", "initPackageDigest"}},
	"init_binary":          {Role: readByTheBuild, Covers: []string{"initBinary", "initBinaryDigest"}},
	"preinstall":           {Role: readByTheBuild, Covers: []string{"preinstall", "preinstallDigest"}},
	"init_go_flags":        {Role: readByTheBuild, Covers: []string{"initGoFlags"}},
	"k3s_binary":           {Role: readByTheBuild, Covers: []string{"k3sBinary", "k3sBinaryDigest"}},
	"k3s_data_dir":         {Role: readByTheBuild, Covers: []string{"k3sDataDir"}},
	"rootfs_label":         {Role: readByTheBuild, Covers: []string{"rootfsLabel"}},
	"rootfs_headroom_mib":  {Role: readByTheBuild, Covers: []string{"rootfsHeadroomMiB"}},
	"vcpu":                 {Role: readByTheBuild, Covers: []string{"vcpu"}},
	"memMiB":               {Role: readByTheBuild, Covers: []string{"memMiB"}},
	"boot_args":            {Role: readByTheBuild, Covers: []string{"bootArgs"}},
	"modules":              {Role: readByTheBuild, Covers: []string{"modules"}},
	"initramfs_modules":    {Role: readByTheBuild, Covers: []string{"initramfsModules"}},
	"images":               {Role: readByTheBuild, Covers: []string{"images"}},
}

func jsonNames(v any) []string {
	out := []string{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func TestEveryFieldTheBuildReadsIsOneTheRecordCovers(t *testing.T) {
	fields := jsonNames(Config{})
	if len(fields) == 0 {
		t.Fatal("the builder's configuration declares no fields, so this test can hold nothing to the " +
			"record and would pass over any input at all")
	}
	recorded := map[string]bool{}
	for _, name := range jsonNames(nodefirecracker.BuildInputs{}) {
		recorded[name] = true
	}
	covered := map[string]bool{}
	for field, declaration := range fieldDeclarations {
		if declaration.Role != readByTheBuild {
			continue
		}
		if len(declaration.Covers) == 0 {
			t.Fatalf("%q is declared read by the build and names no record field that covers it, so "+
				"reading it cannot move an image's name", field)
		}
		for _, name := range declaration.Covers {
			if !recorded[name] {
				t.Fatalf("%q is covered by the record field %q, and nodefirecracker.BuildInputs has "+
					"no such field: a declaration pointing at a field that does not exist covers "+
					"nothing", field, name)
			}
			covered[name] = true
		}
	}
	for _, field := range fields {
		if _, ok := fieldDeclarations[field]; !ok {
			t.Fatalf("the builder's configuration names %q and nothing here says whether the build "+
				"READS it. This is the hole the guest agent and the module tree both fell through: an "+
				"input the build reads and the record does not mention cannot make an image stale, "+
				"because the name an image is given is made of the record alone. Declare it in "+
				"fieldDeclarations as read by the build (with the record fields that cover it), as "+
				"written by the build, or as used elsewhere", field)
		}
	}
	for name := range recorded {
		if !covered[name] {
			t.Fatalf("the record carries %q and nothing is declared to read it. Either an input was "+
				"declared twice or the record kept a field nothing reads, and this test cannot tell "+
				"the two apart -- which is the point: the record and the reads are one set", name)
		}
	}
}

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
