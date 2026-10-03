package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

func platformResult() Result {
	substrates := []string{nodevm.VMMQEMU}
	detail := fmt.Sprintf("this host is %s. It can place nodes with %s; the guest network is %s and "+
		"the accelerator is %s. ", hostos.Name(), strings.Join(substrates, " and "),
		hostos.NetworkMode(), hostos.Accel())
	if hostos.Firecracker() {
		return Result{Label: "host.platform", Kind: Holds,
			Detail: detail + "Firecracker is available here too, so both substrates are run."}
	}
	return Result{Label: "host.platform", Kind: Holds,
		Detail: detail + "Firecracker is a KVM guest and it exists only on Linux, so firecracker " +
			"cannot run here at all: the checks below that are firecracker's are NOT APPLICABLE, " +
			"and qemu is the only substrate this host supports."}
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
					"config's qemu field. Acceleration here would be %s",
					envQEMU, envQEMU, hostos.Accel())}
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
	return Result{Label: "host.qemu", Kind: Holds,
		Detail: detail + fmt.Sprintf(". Acceleration on this host is %s and the guest network is %s",
			hostos.Accel(), hostos.NetworkMode())}
}

func linuxOnlyResults() []Result {
	reason := fmt.Sprintf("this host is %s, and ", hostos.Name())
	return []Result{
		{Label: "host.kernel", Kind: NotApplicable,
			Detail: reason + "the kernel a node's guest boots is the BUILD host's own kernel, " +
				"extracted from /boot. A Windows host has none, so a node image must be built on " +
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
			Detail: reason + "Windows has no pasta and no network namespaces. A qemu node there " +
				"uses qemu's own user-mode networking (slirp), which needs no host interface, tap " +
				"or privilege."},
		{Label: "host.user-namespace", Kind: NotApplicable,
			Detail: reason + "user namespaces are a Linux mechanism pasta builds each guest's " +
				"namespace from, and the user-mode network path does not use them."},
		{Label: "host.kvm", Kind: NotApplicable,
			Detail: reason + "/dev/kvm is Linux's accelerator. This host's qemu uses " +
				hostos.Accel() + ", so a firecracker node cannot run here."},
		{Label: "host.firecracker", Kind: NotApplicable,
			Detail: reason + "Firecracker is a KVM guest and it exists only on Linux; this host " +
				"places nodes with qemu only."},
		{Label: "host.selinux", Kind: NotApplicable,
			Detail: reason + "SELinux is a Linux mechanism, and the guest runs with it off " +
				"regardless, so there is nothing to check here."},
	}
}
