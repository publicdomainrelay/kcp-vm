package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func TestTheManifestRecordsWhatTheBuildRead(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatalf("creating %s: %v", host, err)
	}
	paths := map[string]string{
		"vmlinuz-6.16.4": "this host's kernel, as it was on the day of the build\n",
		"config-6.16.4":  "CONFIG_NETFILTER_XT_TARGET_NFLOG=m\n",
		"base.squashfs":  "the base userspace, as it was on the day of the build\n",
		"k3s":            "k3s version v1.31.1+k3s1\n",
	}
	for name, body := range paths {
		if err := os.WriteFile(filepath.Join(host, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	cfg := validConfig(t)
	cfg.Path = filepath.Join(root, "nodeimage.json")
	cfg.KernelSource = filepath.Join(host, "vmlinuz-6.16.4")
	cfg.KernelConfig = filepath.Join(host, "config-6.16.4")
	cfg.BaseImage = filepath.Join(host, "base.squashfs")
	cfg.K3sBinary = filepath.Join(host, "k3s")
	cfg.Modules = []string{"nf_conntrack", "xt_NFLOG", "xt_physdev"}
	cfg.InitramfsModules = []string{"kernel/drivers/virtio/virtio_mmio"}
	cfg.Images = []string{"rancher/mirrored-pause:3.10.2"}
	encoded, err := json.Marshal(buildConfigOf(cfg))
	if err != nil {
		t.Fatalf("encoding the configuration: %v", err)
	}
	if err := os.WriteFile(cfg.Path, encoded, 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	tars := []imageTar{{
		reference:  cfg.Images[0],
		repository: cfg.Images[0],
		digest:     "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		platform:   "linux/amd64",
		guestName:  imageTarName(cfg.Images[0]),
		bytes:      4096,
	}}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", cfg.OutDir, err)
	}
	if err := writeManifest(cfg, Artifacts{}, tars); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	manifest, err := nodefirecracker.LoadManifest(cfg.OutDir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if manifest.Inputs == nil {
		t.Fatal("the build wrote a manifest that records nothing about what it read, so the " +
			"substrate that boots this image has nothing to hold it to and an image built before " +
			"the configuration changed is indistinguishable from one built after")
	}
	if manifest.Inputs.ConfigPath != cfg.Path {
		t.Fatalf("the manifest records the configuration as %q and the build read %q, so a "+
			"substrate reading the record back would compare the image against a file this build "+
			"never opened", manifest.Inputs.ConfigPath, cfg.Path)
	}
	if err := manifest.CheckInputs(); err != nil {
		t.Fatalf("an image the build had just made from this configuration failed its own "+
			"provenance check, which would mean the record and the configuration disagree about "+
			"inputs the build read from that very file: %v", err)
	}
}
