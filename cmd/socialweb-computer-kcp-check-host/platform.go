package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

func platformResult() Result {
	host := runtime.GOOS + "/" + runtime.GOARCH
	vmm := compiledVMRuntime()
	if vmm == "" {
		return Result{Label: "host.platform", Kind: Holds,
			Detail: fmt.Sprintf("this host is %s, and no VM runtime is compiled into this build for it "+
				"(macOS is plan 0012, Windows is plan 0011), so the checks below that belong to a VM "+
				"runtime are NOT APPLICABLE.", host)}
	}
	return Result{Label: "host.platform", Kind: Holds,
		Detail: fmt.Sprintf("this host is %s, and the VM runtime compiled into this build is %s.", host, vmm)}
}

func qemuResult(f facts) Result {
	path := f.qemu
	if strings.TrimSpace(path) == "" {
		if found, err := exec.LookPath("qemu-system-x86_64"); err == nil {
			path = found
		}
	}
	if strings.TrimSpace(path) == "" {
		return Result{Label: "host.qemu", Kind: f.absent(),
			Detail: f.notHere("the qemu-system binary a qemu node's guest runs on") +
				fmt.Sprintf("no qemu binary is named in %s and none is on PATH. A qemu node is one "+
					"qemu-system process per node, started by the gateway, so without the binary "+
					"there is no node on this substrate at all: name it in %s, or point that at the "+
					"config's qemu field",
					envQEMU, envQEMU)}
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{Label: "host.qemu", Kind: f.absent(),
			Detail: fmt.Sprintf("%s is named as the qemu binary and cannot be read: %v. A qemu node "+
				"is started from this path, so a path that cannot be read is a node that never "+
				"boots", path, err)}
	}
	detail := fmt.Sprintf("%s is present, %s (%04o)", path, mib(info.Size()), info.Mode().Perm())
	if out, err := exec.Command(path, "--version").CombinedOutput(); err == nil {
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if line != "" {
			detail += fmt.Sprintf(", and it answers %s", line)
		}
	}
	return Result{Label: "host.qemu", Kind: Holds, Detail: detail}
}

func linuxOnlyResults() []Result {
	reason := fmt.Sprintf("this host is %s, and ", hostos.Name())
	return []Result{
		{Label: "host.kernel", Kind: NotApplicable,
			Detail: reason + "the kernel a node's guest boots is the BUILD host's own kernel, " +
				"extracted from /boot. This host has none, so a node image must be built on " +
				"Linux, or the kernel and its module tree handed in, before a guest can boot."},
		{Label: "host.kernel-config", Kind: NotApplicable,
			Detail: reason + "/boot/config-<release> is read by k3s's own check-config inside the " +
				"guest, and this platform has no /boot."},
		{Label: "host.module-tree", Kind: NotApplicable,
			Detail: reason + "/lib/modules/<release> is copied into the image whole, and this " +
				"platform has no module tree built beside the kernel."},
		{Label: "host.modules-resolve", Kind: NotApplicable,
			Detail: reason + "the module index this check resolves names against is the Linux " +
				"module tree's."},
		{Label: "host.egress-pasta", Kind: NotApplicable,
			Detail: reason + "pasta and network namespaces are Linux mechanisms, and no guest " +
				"network provider is compiled into this build for this platform."},
		{Label: "host.user-namespace", Kind: NotApplicable,
			Detail: reason + "user namespaces are a Linux mechanism pasta builds each guest's " +
				"namespace from."},
		{Label: "host.kvm", Kind: NotApplicable,
			Detail: reason + "/dev/kvm is Linux's accelerator, so a firecracker node cannot run here."},
		{Label: "host.firecracker", Kind: NotApplicable,
			Detail: reason + "Firecracker is a KVM guest and it exists only on Linux."},
		{Label: "host.selinux", Kind: NotApplicable,
			Detail: reason + "SELinux is a Linux mechanism, and the guest runs with it off " +
				"regardless, so there is nothing to check here."},
	}
}
