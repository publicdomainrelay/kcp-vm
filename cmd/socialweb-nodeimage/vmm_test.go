package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheVMMsTransportIsDerivedFromTheModuleTree(t *testing.T) {
	cfg := validConfig(t)
	if err := checkVMMTransport(cfg); err != nil {
		t.Fatalf("the firecracker fixture names virtio_mmio in a tree that carries it: %v", err)
	}

	qemu := validConfig(t)
	qemu.VMM = "qemu"
	err := checkVMMTransport(qemu)
	if err == nil {
		t.Fatal("a qemu image whose module tree carries neither a built-in nor a loadable " +
			"virtio_pci was accepted: the guest would boot with no block device and panic about " +
			"a root device, and nothing in the guest would name the tree")
	}
	for _, want := range []string{"qemu", "virtio_pci"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	builtin := validConfig(t)
	builtin.VMM = "qemu"
	builtinPath := filepath.Join(builtin.ModuleSource, "modules.builtin")
	raw, err := os.ReadFile(builtinPath)
	if err != nil {
		t.Fatalf("reading %s: %v", builtinPath, err)
	}
	if err := os.WriteFile(builtinPath, append(raw, []byte("kernel/drivers/virtio/virtio_pci.ko\n")...), 0o600); err != nil {
		t.Fatalf("writing %s: %v", builtinPath, err)
	}
	if err := checkVMMTransport(builtin); err != nil {
		t.Fatalf("a qemu image whose kernel carries virtio_pci built in was refused: %v -- a "+
			"built-in transport needs no initramfs module, and requiring one would refuse a "+
			"kernel the real tree says is fine", err)
	}

	unstaged := validConfig(t)
	unstaged.InitramfsModules = []string{"kernel/drivers/net/virtio_net"}
	err = checkVMMTransport(unstaged)
	if err == nil {
		t.Fatal("a firecracker image whose initramfs does not carry the module its transport " +
			"needs was accepted")
	}
	if !strings.Contains(err.Error(), "virtio_mmio") {
		t.Errorf("the refusal does not name the transport the initramfs is missing: %v", err)
	}
}
