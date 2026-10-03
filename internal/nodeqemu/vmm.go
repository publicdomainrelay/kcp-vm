package nodeqemu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

type Platform struct {
	Accel string

	Network string
}

func PlatformNow() Platform {
	return Platform{Accel: hostos.Accel(), Network: hostos.NetworkMode()}
}

func machineOf(accel string) (string, bool) {
	switch accel {
	case hostos.AccelKVM:
		return "q35,accel=kvm", true
	case hostos.AccelWHPX:
		return "q35,accel=whpx:tcg", false
	}
	return "q35,accel=tcg", false
}

func QEMUArgv(params nodevm.NamespaceParams, p Platform) []string {
	machine, cpuHost := machineOf(p.Accel)
	argv := []string{
		"-name", params.Name,
		"-m", strconv.Itoa(params.MemMiB),
		"-smp", strconv.Itoa(params.Vcpu),
		"-machine", machine,
		"-display", "none",
		"-serial", "stdio",
		"-monitor", "none",
	}
	if cpuHost {
		argv = append(argv, "-cpu", "host")
	}
	if params.Kernel != "" {
		argv = append(argv, "-kernel", params.Kernel)
	}
	if params.Initramfs != "" {
		argv = append(argv, "-initrd", params.Initramfs)
	}
	argv = append(argv,
		"-append", params.BootArgs,
		"-drive", "file="+params.Rootfs+",format=raw,if=none,id=rootfs",
		"-device", "virtio-blk-pci,drive=rootfs",
		"-drive", "file="+params.NodeDisk+",format=raw,if=none,id=nodedata,readonly=on",
		"-device", "virtio-blk-pci,drive=nodedata",
	)
	if p.Network == hostos.NetworkUser {
		argv = append(argv,
			"-netdev", "user,id=net0",
			"-device", "virtio-net-pci,netdev=net0,mac="+params.GuestMAC,
		)
	} else {
		argv = append(argv,
			"-netdev", "tap,id=net0,ifname="+params.Tap+",script=no,downscript=no",
			"-device", "virtio-net-pci,netdev=net0,mac="+params.GuestMAC,
		)
	}
	return argv
}

func RunVMM(ctx context.Context, params nodevm.NamespaceParams) error {
	return runQEMU(ctx, params)
}

func runQEMU(ctx context.Context, params nodevm.NamespaceParams) error {
	if params.Name == "" {
		return fmt.Errorf("nodeqemu: the namespace parameters name qemu %q and no node name, and the "+
			"node name is the process's `-name` and therefore its identity: without it nothing can "+
			"tell this guest's VMM from any other on the host, so the record it leaves would name "+
			"a process no later gateway can match", params.VMM)
	}
	console, err := os.OpenFile(params.Console, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("nodeqemu: opening the guest console at %s: %w -- the console is the "+
			"only account of a guest that dies before its agent can report anything, so a boot "+
			"with nowhere to write it is a boot nothing can explain afterwards", params.Console, err)
	}
	defer console.Close()
	vmm := exec.CommandContext(ctx, params.VMM)
	vmm.Args = append([]string{params.VMM}, QEMUArgv(params, PlatformNow())...)
	vmm.Stdout, vmm.Stderr = console, console
	vmm.SysProcAttr = hostos.DetachProcess()
	if err := vmm.Start(); err != nil {
		return fmt.Errorf("nodeqemu: starting %s: %w", params.VMM, err)
	}
	if err := nodevm.WriteInstance(params.Instance, vmm.Process.Pid, time.Now().UnixNano()); err != nil {
		_ = vmm.Process.Kill()
		return err
	}
	return vmm.Wait()
}
