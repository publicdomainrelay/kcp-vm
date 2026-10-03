package nodeqemu

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

func validParams(t *testing.T) nodevm.NamespaceParams {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"vmlinux", "initramfs.cpio", "rootfs.ext4", "node-disk.ext4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}
	return nodevm.NamespaceParams{
		Kind:      nodevm.KindQEMU,
		VMM:       "/usr/bin/qemu-system-x86_64",
		Name:      "liveweb-node1",
		Kernel:    filepath.Join(dir, "vmlinux"),
		Initramfs: filepath.Join(dir, "initramfs.cpio"),
		Rootfs:    filepath.Join(dir, "rootfs.ext4"),
		NodeDisk:  filepath.Join(dir, "node-disk.ext4"),
		BootArgs:  "console=ttyS0 root=/dev/vda",
		Console:   filepath.Join(dir, "guest.console"),
		Instance:  filepath.Join(dir, "instance.json"),
		Vcpu:      2,
		MemMiB:    2048,
		Tap:       nodevm.TapName,
		Bridge:    nodevm.BridgeName,
		GuestMAC:  nodevm.DefaultGuestMAC,
	}
}

func TestTheQEMUCommandLineCarriesTheGuestsDisksAndTap(t *testing.T) {
	params := validParams(t)
	argv := QEMUArgv(params, Platform{Accel: hostos.AccelKVM, Network: hostos.NetworkPasta})
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-name liveweb-node1",
		"-kernel " + params.Kernel,
		"-initrd " + params.Initramfs,
		"-append " + params.BootArgs,
		"file=" + params.Rootfs + ",format=raw,if=none,id=rootfs",
		"virtio-blk-pci,drive=rootfs",
		"file=" + params.NodeDisk + ",format=raw,if=none,id=nodedata,readonly=on",
		"virtio-blk-pci,drive=nodedata",
		"tap,id=net0,ifname=" + nodevm.TapName + ",script=no,downscript=no",
		"virtio-net-pci,netdev=net0,mac=" + nodevm.DefaultGuestMAC,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the qemu command line does not carry %q, so the guest boots without it: %v",
				want, argv)
		}
	}
	if !strings.Contains(joined, strconv.Itoa(params.MemMiB)) ||
		!strings.Contains(joined, strconv.Itoa(params.Vcpu)) {
		t.Errorf("the qemu command line does not carry the machine size %d MiB / %d vcpu: %v",
			params.MemMiB, params.Vcpu, argv)
	}
}

func TestTheQEMUIdentityIsTheNodeNameArgument(t *testing.T) {
	got := Identity("/state/liveweb/nodes/liveweb-node1")
	want := []string{"-name", "liveweb-node1"}
	if len(got) != len(want) {
		t.Fatalf("Identity = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Identity = %v, want %v: the node name is the `-name` argument qemu is "+
				"started with, and it is what separates this node's VMM from every other one on "+
				"the host", got, want)
		}
	}
	joined := strings.Join(QEMUArgv(validParams(t), Platform{Accel: hostos.AccelKVM, Network: hostos.NetworkPasta}), " ")
	if !strings.Contains(joined, "-name liveweb-node1") {
		t.Fatalf("the qemu command line does not carry the identity %v, so the record the "+
			"host writes could never be matched to a live process: %v", want, joined)
	}
}

func TestTheSubstratesAccelerationAndNetworkAreChosenFromThePlatform(t *testing.T) {
	kvm := strings.Join(QEMUArgv(validParams(t), Platform{Accel: hostos.AccelKVM, Network: hostos.NetworkPasta}), " ")
	if !strings.Contains(kvm, "-machine q35,accel=kvm") || !strings.Contains(kvm, "-cpu host") {
		t.Fatalf("a kvm boot was not given kvm acceleration with -cpu host: %v", kvm)
	}
	if !strings.Contains(kvm, "-netdev tap,id=net0,ifname=fctap0") {
		t.Fatalf("a pasta boot was not given a tap netdev: %v", kvm)
	}
	whpx := strings.Join(QEMUArgv(validParams(t), Platform{Accel: hostos.AccelWHPX, Network: hostos.NetworkUser}), " ")
	if !strings.Contains(whpx, "-machine q35,accel=whpx:tcg") {
		t.Fatalf("a whpx boot was not given whpx with a tcg fallback: %v", whpx)
	}
	if strings.Contains(whpx, "-cpu host") {
		t.Fatalf("a whpx boot was given -cpu host, which is not the guest's cpu model: %v", whpx)
	}
	if !strings.Contains(whpx, "-netdev user,id=net0") {
		t.Fatalf("a user-mode boot was not given qemu's slirp netdev: %v", whpx)
	}
	if strings.Contains(whpx, "ifname=") {
		t.Fatalf("a user-mode boot names a tap interface, and the user-mode path has no tap: %v", whpx)
	}
	tcg := strings.Join(QEMUArgv(validParams(t), Platform{Accel: hostos.AccelTCG, Network: hostos.NetworkPasta}), " ")
	if !strings.Contains(tcg, "-machine q35,accel=tcg") {
		t.Fatalf("a tcg boot was not given the tcg fallback: %v", tcg)
	}
	if strings.Contains(tcg, "-cpu host") {
		t.Fatalf("a tcg boot was given -cpu host, which tcg cannot honour, so the fallback "+
			"would fail rather than run slowly: %v", tcg)
	}
}

func TestRunVMMStartsTheFakeExecutableAndPublishesItsPid(t *testing.T) {
	params := validParams(t)
	dir := filepath.Dir(params.Instance)
	argvPath := filepath.Join(dir, "received")
	vmm := filepath.Join(dir, "fake-qemu")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvPath + "\nsleep 0.2\n"
	if err := os.WriteFile(vmm, []byte(script), 0o755); err != nil {
		t.Fatalf("staging a stand-in for qemu: %v", err)
	}
	params.VMM = vmm

	if err := runQEMU(context.Background(), params); err != nil {
		t.Fatalf("runQEMU with a fake VMM: %v", err)
	}
	raw, err := os.ReadFile(params.Instance)
	if err != nil {
		t.Fatalf("the VMM's pid was not published at %s: %v", params.Instance, err)
	}
	var rec nodevm.InstanceRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("the instance record at %s: %v", params.Instance, err)
	}
	if rec.PID <= 0 {
		t.Fatalf("the instance record names pid %d, and a pid of zero names no process to record "+
			"or reap", rec.PID)
	}
	received, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("the fake VMM was not handed a command line: %v", err)
	}
	if !strings.Contains(string(received), "-name\nliveweb-node1") {
		t.Fatalf("the fake VMM received %q, which does not carry the identity the record is "+
			"matched by", received)
	}
}
