package nodeqemu

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

const (
	liveImageEnv = "SOCIALWEB_NODEQEMU_IMAGE"
	liveAgentEnv = "SOCIALWEB_NODEQEMU_AGENT"
	liveBinEnv   = "SOCIALWEB_NODEQEMU_BIN"
)

func TestALiveQEMUGuestBootsAndIsReaped(t *testing.T) {
	livegate.Short(t, "this test boots a real qemu guest in a real pasta network namespace and "+
		"-short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 "+
		"to fail instead")
	image := os.Getenv(liveImageEnv)
	agent := os.Getenv(liveAgentEnv)
	qemu := os.Getenv(liveBinEnv)
	if qemu == "" {
		qemu = "/usr/bin/qemu-system-x86_64"
	}
	if image == "" || agent == "" {
		livegate.Require(t, "set %s, %s and %s to a built node image, the agent built for the "+
			"guest and a qemu-system binary, and this test boots ONE guest in its own pasta "+
			"namespace on this host: it measures that qemu is started with the guest's image and "+
			"network and that the node is reaped by identity. It is skipped by default because it "+
			"needs a host with pasta, kvm, the image and the agent, and it leaves nothing behind",
			liveImageEnv, liveAgentEnv, liveBinEnv)
	}
	for _, req := range []struct{ name, value string }{
		{liveImageEnv, image},
		{liveAgentEnv, agent},
	} {
		if req.value == "" {
			t.Fatalf("%s is not set", req.name)
		}
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		livegate.Require(t, "/dev/kvm is not available (%v), so this test cannot boot a guest "+
			"with kvm", err)
	}
	if _, err := exec.LookPath("pasta"); err != nil {
		livegate.Require(t, "pasta is not on this host's PATH: %v", err)
	}
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		livegate.Require(t, "mkfs.ext4 is not on this host's PATH: %v", err)
	}
	kernel := filepath.Join(image, "vmlinux")
	initramfs := filepath.Join(image, "initramfs.cpio")
	rootfs := filepath.Join(image, "rootfs.ext4")
	for _, req := range []struct{ name, path string }{
		{"kernel", kernel},
		{"initramfs", initramfs},
		{"rootfs", rootfs},
	} {
		if _, err := os.Stat(req.path); err != nil {
			livegate.Require(t, "the image at %s has no %s (%v), so this test cannot boot it",
				image, req.name, err)
		}
	}

	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("K10qemu-live-token"), 0o600); err != nil {
		t.Fatalf("staging the join token: %v", err)
	}
	dataDir := filepath.Join(dir, "nodes", "liveweb-node1")
	host, err := New(Options{
		Cluster:   "liveweb",
		Server:    "https://10.100.0.1:24000",
		NodeName:  "liveweb-node1",
		TokenFile: token,
		DataDir:   dataDir,
		Record:    func(context.Context, string, v1alpha1.NodePhase) error { return nil },
		Agent:     agent,
		QEMU:      qemu,
		Image:     nodevm.Image{Kernel: kernel, Initramfs: initramfs, Rootfs: rootfs},
		Guest:     nodevm.Guest{Vcpu: 1, MemMiB: 512, BootArgs: []string{"console=ttyS0", "selinux=0", "root=/dev/vda"}},
		Network: nodevm.Network{
			Gateway: "10.100.0.1",
			GuestIP: "10.100.0.2",
			Prefix:  24,
			PastaIP: "10.100.0.0",
		},
	})
	if err != nil {
		livegate.Require(t, "the qemu placement was refused before anything was booted: %v", err)
	}
	ctx := context.Background()
	if err := host.Provision(ctx); err != nil {
		_ = host.Teardown(ctx)
		t.Fatalf("provisioning a qemu guest: %v", err)
	}
	live, err := host.Alive()
	if err != nil {
		t.Fatalf("Alive: %v", err)
	}
	if live != noderecord.LivenessOurs {
		t.Fatalf("the qemu guest this process started reports liveness %v, want ours", live)
	}
	if err := host.Teardown(ctx); err != nil {
		t.Fatalf("tearing the qemu guest down: %v", err)
	}
	if _, err := os.Stat(noderecord.RecordPath(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("the node record survived the teardown: %v", err)
	}
}
