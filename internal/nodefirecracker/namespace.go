package nodefirecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

func RunVMM(ctx context.Context, params nodevm.NamespaceParams) error {
	return runVMM(ctx, params)
}

func runVMM(ctx context.Context, params namespaceParams) error {
	if params.Socket == "" {
		return fmt.Errorf("nodefirecracker: the namespace parameters name firecracker %q and socket "+
			"%q, and a firecracker VMM with no API socket is one nothing can configure or talk to",
			params.VMM, params.Socket)
	}
	console, err := os.OpenFile(params.Console, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("nodefirecracker: opening the guest console at %s: %w -- the console is "+
			"the only account of a guest that dies before its agent can report anything, so a boot "+
			"with nowhere to write it is a boot nothing can explain afterwards",
			params.Console, err)
	}
	defer console.Close()
	if err := os.Remove(params.Socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("nodefirecracker: clearing a previous API socket at %s: %w -- a leftover "+
			"socket file is a path that dials nowhere, and a VMM configured against it would look "+
			"started while the guest was never created", params.Socket, err)
	}
	vmm := exec.CommandContext(ctx, params.VMM)
	vmm.Args = []string{"firecracker", "--api-sock", params.Socket}
	vmm.Stdout, vmm.Stderr = console, console
	vmm.SysProcAttr = hostos.DetachProcess()
	if err := vmm.Start(); err != nil {
		return fmt.Errorf("nodefirecracker: starting %s: %w", params.VMM, err)
	}
	if instanceStart, err := configure(ctx, params); err != nil {
		_ = vmm.Process.Kill()
		return err
	} else if err := nodevm.WriteInstance(params.Instance, vmm.Process.Pid, instanceStart); err != nil {
		_ = vmm.Process.Kill()
		return err
	}
	return vmm.Wait()
}

func configure(ctx context.Context, params namespaceParams) (int64, error) {
	client := NewClient(params.Socket)
	if err := client.WaitForSocket(ctx, 20*time.Second); err != nil {
		return 0, err
	}
	if err := client.MachineConfig(ctx, params.Vcpu, params.MemMiB, params.SMP); err != nil {
		return 0, err
	}
	if err := client.BootSource(ctx, params.Kernel, params.BootArgs, params.Initramfs); err != nil {
		return 0, err
	}
	if err := client.Drive(ctx, "rootfs", params.Rootfs, true, false); err != nil {
		return 0, err
	}
	if err := client.Drive(ctx, "nodedata", params.NodeDisk, false, true); err != nil {
		return 0, err
	}
	if err := client.NetworkInterface(ctx, DefaultInterface, params.Tap, params.GuestMAC); err != nil {
		return 0, err
	}
	instanceStart := time.Now().UnixNano()
	if err := client.InstanceStart(ctx); err != nil {
		return 0, err
	}
	return instanceStart, nil
}

func wireNamespace(ctx context.Context, params namespaceParams) error {
	return nodevm.WireNamespace(ctx, params)
}

func ip(ctx context.Context, args ...string) error {
	return nodevm.IP(ctx, args...)
}
