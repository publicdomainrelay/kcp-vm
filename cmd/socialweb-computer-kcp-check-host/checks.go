package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kernelmodules"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/liveports"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func run(f facts) []Result {
	results := []Result{}
	if len(f.configMissing) > 0 {
		results = append(results, configResult(f))
	}
	results = append(results, platformResult(), qemuResult(f))
	if compiledVMRuntime() != "" {
		results = append(results,
			kernelResult(f),
			kernelConfigResult(f),
			moduleTreeResult(f),
			modulesResolveResult(f),
			pastaResult(f),
			userNamespaceResult(f),
			kvmResult(f),
			firecrackerResult(f),
			selinuxResult(f),
		)
	} else {
		results = append(results, linuxOnlyResults()...)
	}
	results = append(results,
		k3sResult(f),
		kubectlResult(f),
		kineResult(f),
		kcpResult(f),
		imageStoreResult(f),
		poolResult(f),
		diskResult(f),
		postgresResult(f),
		liveportsResult(f),
		nodePasswordResult(f),
	)
	return results
}

func configResult(f facts) Result {
	where := f.configPath
	if where == "" {
		where = "no config path was named"
	}
	return Result{
		Label: "host.config",
		Kind:  f.absent(),
		Detail: f.notHere("the operator's config") + fmt.Sprintf("%s does not name %s. Without it "+
			"every path below came from an environment override or from a default computed off the "+
			"release this host runs, and the checks that need an operator's decision -- the image "+
			"store's output path, the VMM binary, the module list the image names -- have nothing to "+
			"hold to. Point %s or -config at the config this host was built from: a check that "+
			"invents the paths it is checking is a check that passes on a host nobody is going to "+
			"provision on", where, strings.Join(f.configMissing, ", "), envConfigPath),
	}
}

func releaseOf(path string) string {
	base := filepath.Base(path)
	for _, prefix := range []string{"vmlinuz-", "config-"} {
		base = strings.TrimPrefix(base, prefix)
	}
	return base
}

func kernelResult(f facts) Result {
	size, err := readableFile(f.kernel)
	if err != nil {
		return Result{
			Label: "host.kernel",
			Kind:  f.absent(),
			Detail: f.notHere("the kernel a guest would boot") + fmt.Sprintf("no readable kernel at "+
				"%s: %v. A guest runs THE HOST'S OWN KERNEL: the image carries this host's kernel "+
				"and the module tree built beside it, so a container -- a machine with no /boot of "+
				"this host's kernel -- has to be handed it, and a host that has lost it cannot build "+
				"an image at all. Mount the host's /boot at /boot, or name a copy in %s. Nothing else "+
				"stands in: a kernel of another release carries neither the modules the guest loads "+
				"nor the config k3s's own check-config reads, and the guest that boots from it says "+
				"so only on its own console", f.kernel, err, envKernel),
		}
	}
	detail := fmt.Sprintf("%s is readable, %s, and it is the kernel this host runs: %s",
		f.kernel, mib(size), f.release)
	if got := releaseOf(f.kernel); got != f.release {
		detail += fmt.Sprintf(". Note that the file carries the release %s while this host runs %s, "+
			"so the modules that can load in a guest booting it are the tree built beside %s, and "+
			"not the tree this host is loading from now", got, f.release, got)
	}
	return Result{Label: "host.kernel", Kind: Holds, Detail: detail}
}

func kernelConfigResult(f facts) Result {
	size, err := readableFile(f.kernelConfig)
	if err != nil {
		return Result{
			Label: "host.kernel-config",
			Kind:  f.absent(),
			Detail: f.notHere("the config of the kernel a guest would boot") + fmt.Sprintf("no "+
				"readable kernel config at %s: %v. The guest's k3s runs its own check-config against "+
				"this file, and the image stages it beside the kernel, so it is passed in the same "+
				"way the kernel is: %s, or mount the host's /boot. Without it the config the kernel "+
				"was BUILT with is unknown, and every question about whether a netfilter feature is "+
				"present becomes a guess from a module name", f.kernelConfig, err, envKernelConfig),
		}
	}
	return Result{
		Label: "host.kernel-config",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s is readable, %s, and it is the config of the kernel this host runs "+
			"(%s)", f.kernelConfig, mib(size), releaseOf(f.kernelConfig)),
	}
}

func moduleTreeResult(f facts) Result {
	tree, err := kernelmodules.LoadTree(f.moduleSource)
	if err != nil {
		return Result{
			Label: "host.module-tree",
			Kind:  f.absent(),
			Detail: f.notHere("the module tree built beside the kernel a guest boots") +
				fmt.Sprintf("the tree under %s is not readable here: %v. The modules a guest loads "+
					"are the ones built beside the kernel it boots, so this tree is a property of this "+
					"host's kernel and nothing in a container produces one: %s, or mount the host's "+
					"/lib/modules. Every name in the image's module list resolves against THIS tree or "+
					"it resolves against nothing", f.moduleSource, err, envModuleSource),
		}
	}
	return Result{
		Label: "host.module-tree",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s carries %d module files, %d built-ins and %d aliases, and it is the "+
			"tree built beside the kernel this host runs (%s)", f.moduleSource, tree.Files(),
			tree.Builtins(), tree.Aliases(), f.release),
	}
}

func modulesResolveResult(f facts) Result {
	source := f.moduleSource
	where := "the module tree the build reads"
	if f.stageDir != "" {
		staged := filepath.Join(f.stageDir, "lib", "modules", filepath.Base(f.moduleSource))
		if isDir(staged) {
			source = staged
			where = "the staged tree the guest boots from"
		}
	}
	if len(f.modules) == 0 {
		return Result{
			Label: "host.modules-resolve",
			Kind:  f.absent(),
			Detail: f.notHere("the module list the image names") + "the names are the operator's, " +
				"they live in the config (modules), and no config is in hand. This check exists " +
				"because a list that was one name short cost a session: nothing on this host says so, " +
				"the build succeeds, and the guest reports it on a console nobody reads until a " +
				"Service has no address. Pass the config in -- or the names -- and this line becomes " +
				"the check that catches it before the image exists",
		}
	}
	if err := kernelmodules.Verify(source, f.modules); err != nil {
		return Result{
			Label:  "host.modules-resolve",
			Kind:   Missing,
			Detail: fmt.Sprintf("%s does not carry the module list this image names: %v", where, err),
		}
	}
	return Result{
		Label: "host.modules-resolve",
		Kind:  Holds,
		Detail: fmt.Sprintf("all %d names this image lists resolve in %s, and every iptables "+
			"extension the cluster reaches for is provided by one of them or built into the kernel",
			len(f.modules), where),
	}
}

func pastaResult(f facts) Result {
	path, err := exec.LookPath(nodefirecracker.DefaultPasta)
	if err != nil {
		return Result{
			Label: "host.egress-pasta",
			Kind:  Missing,
			Detail: fmt.Sprintf("no %s on PATH: %v. Every guest reaches the network through it, and "+
				"it is what builds the guest's own network namespace by copying this host's -- the "+
				"agent dials the server it was given from inside that namespace, so a host without "+
				"pasta is a host whose nodes never register", nodefirecracker.DefaultPasta, err),
		}
	}
	detail := path + " is present"
	if out, err := exec.Command(path, "--version").CombinedOutput(); err == nil {
		if line := firstLine(string(out)); line != "" {
			detail += ", " + line
		}
	}
	return Result{Label: "host.egress-pasta", Kind: Holds, Detail: detail}
}

func userNamespaceResult(f facts) Result {
	max := procValue("/proc/sys/user/max_user_namespaces")
	clone := procValue("/proc/sys/kernel/unprivileged_userns_clone")
	probe, err := exec.LookPath("unshare")
	if err != nil {
		return Result{
			Label: "host.user-namespace",
			Kind:  f.absent(),
			Detail: f.notHere("util-linux's unshare, which is the only way to ASK this question "+
				"without root") + fmt.Sprintf("/proc/sys/user/max_user_namespaces says %s, and that "+
				"number is not the answer: a runtime can refuse the clone a user namespace needs "+
				"without ever changing it, and the refusal is what this check exists to see. Install "+
				"util-linux and run this again", orUnknown(max)),
		}
	}
	out, err := exec.Command(probe, "--user", "--map-root-user", "--net", "true").CombinedOutput()
	if err != nil {
		return Result{
			Label: "host.user-namespace",
			Kind:  f.absent(),
			Detail: fmt.Sprintf("CLONE_NEWUSER with CLONE_NEWNET was REFUSED to this user: %v: %s. "+
				"/proc/sys/user/max_user_namespaces says %s and "+
				"/proc/sys/kernel/unprivileged_userns_clone says %s, which is the point of probing "+
				"rather than reading: the count was permission enough and the runtime still said no. "+
				"Every guest runs in a namespace pasta builds inside one of these, so this is a "+
				"machine that cannot provision a node. On a host, that is this environment failing "+
				"to be what the design needs; inside a container it is the boundary being visible, "+
				"and the node half is the half such a container cannot run",
				err, strings.TrimSpace(string(out)), orUnknown(max), orUnknown(clone)),
		}
	}
	return Result{
		Label: "host.user-namespace",
		Kind:  Holds,
		Detail: fmt.Sprintf("this user can create a user namespace and a network namespace: unshare "+
			"--user --map-root-user --net exits 0. /proc/sys/user/max_user_namespaces says %s and "+
			"/proc/sys/kernel/unprivileged_userns_clone says %s, neither of which is the load-bearing "+
			"line: the probe is, because a runtime forbids the clone without touching either number",
			orUnknown(max), orUnknown(clone)),
	}
}

func kvmResult(f facts) Result {
	info, err := os.Stat(f.kvm)
	if err != nil {
		return Result{
			Label: "host.kvm",
			Kind:  f.absent(),
			Detail: f.notHere("the KVM device a guest runs on") + fmt.Sprintf("%s is not here: %v. "+
				"It is the only way a guest runs, it exists only on the host that owns the CPUs, and "+
				"no container can be given it by installing anything: pass it with --device %s. "+
				"Without it there is no node, no matter what else this host checks out",
				f.kvm, err, f.kvm),
		}
	}
	st, ok := readDevice(info)
	if !ok {
		return Result{Label: "host.kvm", Kind: f.absent(),
			Detail: fmt.Sprintf("%s exists and this build cannot read the stat that describes it, "+
				"so its mode and its owner are unknown and its openness cannot be reported", f.kvm)}
	}
	named := deviceDescription(info, st)
	fd, err := os.OpenFile(f.kvm, os.O_RDWR, 0)
	if err != nil {
		return Result{
			Label: "host.kvm",
			Kind:  f.absent(),
			Detail: fmt.Sprintf("%s is %s and this user cannot open it read-write: %v. A guest is "+
				"started by a VMM that opens it read-write, so readability is not the question. On a "+
				"host this is a udev rule and a group membership away; in a container it is a device "+
				"that has to be passed in with --device %s, and the container's user has to be in the "+
				"group the device is owned by", f.kvm, named, err, f.kvm),
		}
	}
	fd.Close()
	return Result{
		Label: "host.kvm",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s is %s and this user opened it read-write, and that mode is why "+
			"this check reports what it measured rather than that it is readable: a permissive mode "+
			"makes a read claim pass for a user in no group at all, and a container that is given "+
			"the device inherits that as measured, not as intended", f.kvm, named),
	}
}

func firecrackerResult(f facts) Result {
	if f.firecracker == "" {
		return Result{
			Label: "host.firecracker",
			Kind:  f.absent(),
			Detail: f.notHere("the VMM binary") + fmt.Sprintf("no config in hand names one and %s is "+
				"unset. Firecracker is installed on the host that owns the CPUs and is started beside "+
				"/dev/kvm, which is the pair a container cannot have of its own: pass the binary and "+
				"the device together, or run the node half on the host", envFirecracker),
		}
	}
	info, err := os.Stat(f.firecracker)
	if err != nil {
		return Result{
			Label: "host.firecracker",
			Kind:  f.absent(),
			Detail: f.notHere("the VMM binary") + fmt.Sprintf("%s is not here: %v. It is where the "+
				"configuration says the VMM is, and a path a build was not told about is a path where "+
				"a caller cannot find it either: the live node tests take a firecracker path from %s, "+
				"the same way this check does", f.firecracker, err, envFirecracker),
		}
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return Result{
			Label: "host.firecracker",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s is not an executable file (mode %04o), so the path the "+
				"configuration names holds something a VMM cannot be started from",
				f.firecracker, info.Mode().Perm()),
		}
	}
	detail := fmt.Sprintf("%s is present, %s (%04o)", f.firecracker, mib(info.Size()), info.Mode().Perm())
	if out, err := exec.Command(f.firecracker, "--version").CombinedOutput(); err == nil {
		if line := firstLine(string(out)); line != "" {
			detail += ", " + line
		}
	}
	if found, err := exec.LookPath("firecracker"); err == nil && found != f.firecracker {
		detail += fmt.Sprintf(". Note that PATH resolves firecracker to %s instead, and nothing "+
			"here reads the configuration: whichever caller starts a VMM decides which of the two "+
			"runs", found)
	}
	return Result{Label: "host.firecracker", Kind: Holds, Detail: detail}
}

func k3sResult(f facts) Result {
	notes := []string{}
	named := false
	detail := ""
	if f.k3s == "" {
		notes = append(notes, "no config in hand names a k3s_binary")
	} else if info, err := os.Stat(f.k3s); err != nil {
		notes = append(notes, fmt.Sprintf("the configuration names %s and nothing is there: %v", f.k3s, err))
	} else {
		named = true
		detail = fmt.Sprintf("%s %s", f.k3s, mib(info.Size()))
		if out, err := exec.Command(f.k3s, "--version").CombinedOutput(); err == nil {
			detail += ", " + firstLine(string(out))
		}
	}
	onPath, pathErr := exec.LookPath("k3s")
	if !named {
		if pathErr != nil {
			notes = append(notes, "and no k3s on PATH, which is where the cluster's own argv "+
				"resolves it by name")
			return Result{
				Label: "host.k3s",
				Kind:  Missing,
				Detail: fmt.Sprintf("the cluster's control plane is a real k3s server, and this "+
					"environment has none: %s. Nothing else stands in for it -- the reconciler drives "+
					"the server it is given, and a host without one reaches no Ready phase at all, "+
					"which reads as a broken reconciler in whatever was committed last",
					strings.Join(notes, "; ")),
			}
		}
		detail = onPath
		if out, err := exec.Command(onPath, "--version").CombinedOutput(); err == nil {
			detail += ", " + firstLine(string(out))
		}
		notes = append(notes, "this one came from PATH")
	} else if pathErr == nil && onPath != f.k3s {
		detail += ". Note that PATH resolves k3s to " + onPath + " while the configuration names " +
			f.k3s + ", and the server argv says k3s and lets PATH decide, so the configuration is " +
			"not what runs"
	}
	if len(notes) > 0 {
		detail += ". Note: " + strings.Join(notes, "; ")
	}
	return Result{Label: "host.k3s", Kind: Holds, Detail: pinned("k3s", detail)}
}

func kubectlResult(f facts) Result {
	path := f.kubectl
	if path == "" {
		found, err := exec.LookPath("kubectl")
		if err != nil {
			return Result{
				Label: "host.kubectl",
				Kind:  Missing,
				Detail: fmt.Sprintf("no kubectl on PATH: %v. The live tests read the guest's and the "+
					"cluster's state with it, and a kubectl of the wrong minor against a server is "+
					"the difference between a proof that measures the cluster and a proof that "+
					"measures its own client library. Install it, or name it in %s", err, envKubectl),
			}
		}
		path = found
	}
	detail := path
	if out, err := exec.Command(path, "version", "--client").CombinedOutput(); err == nil {
		detail += ", " + clientVersion(string(out))
	} else {
		detail += fmt.Sprintf(" (version --client failed: %v)", err)
	}
	return Result{Label: "host.kubectl", Kind: Holds, Detail: pinned("kubectl", detail)}
}

func kineResult(f facts) Result {
	path := f.kine
	if path == "" {
		found, err := exec.LookPath("kine")
		if err != nil {
			return Result{
				Label: "host.kine",
				Kind:  Missing,
				Detail: fmt.Sprintf("no kine on PATH: %v. Every cluster's store is postgres behind "+
					"kine, the server argv says kine and lets PATH decide, and kcp dials the same "+
					"kind of endpoint: a host without it has no datastore for either half. Build it "+
					"into the environment (or set %s)", err, envKine),
			}
		}
		path = found
	}
	detail := path
	if out, err := exec.Command(path, "--version").CombinedOutput(); err == nil {
		detail += ", " + firstLine(string(out))
	}
	return Result{Label: "host.kine", Kind: Holds, Detail: pinned("kine", detail)}
}

func kcpResult(f facts) Result {
	path := f.kcp
	if !filepath.IsAbs(path) {
		found, err := exec.LookPath(path)
		if err != nil {
			return Result{
				Label: "host.kcp",
				Kind:  Missing,
				Detail: fmt.Sprintf("no kcp at %s and none on PATH: %v. kcp is the control plane the "+
					"tenants are addressed through, and the one-container deployment carries it. A "+
					"container can carry it -- it is a plain binary and it runs as any user with its "+
					"own root directory -- so this is a MISSING and not something to be passed in "+
					"from a host that happens to have one already", path, err),
			}
		}
		path = found
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{
			Label: "host.kcp",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s is not here: %v. The one-container deployment carries kcp and "+
				"starts an instance with its own root directory, so a host without it has no tenant "+
				"path at all", path, err),
		}
	}
	detail := fmt.Sprintf("%s is present, %s (%04o)", path, mib(info.Size()), info.Mode().Perm())
	detail = pinned("kcp", detail)
	if invariant, err := pinnedInvariant("kcp"); err == nil && invariant != "" {
		detail += ". internal/versions records the deployment's invariant as: " + invariant
	}
	return Result{Label: "host.kcp", Kind: Holds, Detail: detail}
}

func imageStoreResult(f facts) Result {
	if f.outDir == "" {
		return Result{
			Label: "host.image-store",
			Kind:  f.absent(),
			Detail: f.notHere("the image store") + fmt.Sprintf("no config in hand names out_dir and "+
				"%s is unset. The store is where a built image lands and where the provisioner reads "+
				"one from, one directory per fingerprint, and an environment that is given the store "+
				"is an environment that can run the tests that need an image", envOutDir),
		}
	}
	if f.configPath == "" || len(f.configMissing) > 0 {
		return Result{
			Label: "host.image-store",
			Kind:  f.absent(),
			Detail: f.notHere("the configuration the image is named by") + fmt.Sprintf("the store at "+
				"%s is keyed by the SHA-256 of everything the build read, and the provisioner derives "+
				"that name from the config (%s) rather than from a setting, so a store and no config "+
				"is a store whose image cannot be shown to be current: a directory that exists and "+
				"validates is exactly what a stale image looks like", f.outDir, envConfigPath),
		}
	}
	image, manifest, err := nodefirecracker.LoadImage(f.outDir, f.configPath)
	if err != nil {
		var differ *nodefirecracker.InputsDifferError
		if errors.As(err, &differ) {
			return Result{
				Label: "host.image-store",
				Kind:  Missing,
				Detail: fmt.Sprintf("the store at %s holds the image this configuration names and "+
					"that image is NOT the one this configuration describes: %v. This is the hole the "+
					"fingerprint exists to close and the one it cannot close on its own -- a build "+
					"system only hashes what is inside its build context, and the configuration this "+
					"image is named by lives outside this repository, so an edit to it can be "+
					"answered by a cache hit that rebuilds nothing while the image keeps the name a "+
					"build gave it", f.outDir, err),
			}
		}
		return Result{
			Label: "host.image-store",
			Kind:  f.absent(),
			Detail: f.notHere("the image this configuration names") + fmt.Sprintf("the store at %s "+
				"does not hold it: %v", f.outDir, err),
		}
	}
	recorded, err := manifest.RecordedFingerprint()
	if err != nil {
		return Result{
			Label: "host.image-store",
			Kind:  Missing,
			Detail: fmt.Sprintf("the image this configuration names is on this host and its record of "+
				"its own inputs cannot be hashed: %v. The record is what says the image on this host "+
				"is the one a build of this configuration produced, so an image whose name cannot be "+
				"recomputed from the record is one no host can be shown to have the right copy of",
				err),
		}
	}
	return Result{
		Label: "host.image-store",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s holds the image this configuration names, and the image's own "+
			"record of its inputs hashes to that name: %s (%s), %s, %s and %s, and its guest "+
			"document is the one the provisioner reads (%s). The name is the SHA-256 of the "+
			"inputs -- this host's kernel and its config, the module tree copied whole, the base "+
			"image, the k3s binary, the built agent, the seeded images and the module list -- so an "+
			"image under this name is one a build of THESE inputs produced, and the fingerprint is "+
			"what a host-extracted artifact is checked against rather than what orders a build",
			f.outDir, recorded, mib(imageBytes(image)), image.Kernel, image.Initramfs,
			image.Rootfs, describeStore(f.outDir)),
	}
}

func imageBytes(image nodefirecracker.Image) int64 {
	total := int64(0)
	for _, path := range []string{image.Kernel, image.Initramfs, image.Rootfs} {
		if info, err := os.Stat(path); err == nil {
			total += info.Size()
		}
	}
	return total
}

func describeStore(outDir string) string {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return "a store that cannot be listed"
	}
	whole := 0
	total := int64(0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if size, ok := imageSize(filepath.Join(outDir, entry.Name())); ok {
			whole++
			total += size
		}
	}
	return fmt.Sprintf("%d complete image(s), %s", whole, mib(total))
}

func liveportsResult(f facts) Result {
	dir := liveports.LockDir()
	probe := filepath.Join(dir, ".check-host")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{
			Label: "host.liveports-lock",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s is the directory the live proofs take their port slots in, and "+
				"it cannot be created here: %v. Two runs that cannot see each other take one slot, "+
				"and the loser does not fail to bind -- it fails its readiness deadline, which reads "+
				"as a broken control plane in whatever was committed last. A container has its own "+
				"/var/tmp, so this is a directory it can create for itself: it is MISSING only when "+
				"the container's own filesystem refuses it", dir, err),
		}
	}
	fd, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Result{
			Label: "host.liveports-lock",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s exists and a lock file cannot be written in it: %v. The proofs "+
				"that bind real ports take a slot here before they bind anything, and a slot claimed "+
				"by a run that cannot write its claim is a slot two runs believe they hold", dir, err),
		}
	}
	fd.Close()
	os.Remove(probe)
	return Result{
		Label: "host.liveports-lock",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s exists and is writable by this user: a proof can claim a slot, so "+
			"two runs on this host cannot take the same ports", dir),
	}
}

func nodePasswordResult(f facts) Result {
	fd, err := os.OpenFile(f.password, os.O_RDWR, 0)
	if err == nil {
		info, statErr := fd.Stat()
		fd.Close()
		size := int64(-1)
		mode := os.FileMode(0)
		if statErr == nil {
			size = info.Size()
			mode = info.Mode().Perm()
		}
		if size == 0 {
			return Result{
				Label: "host.k3s-node-password",
				Kind:  Missing,
				Detail: fmt.Sprintf("%s exists and is EMPTY, which is worse than absent: it opens "+
					"read-write, so every check that asks whether the file can be written says yes, and "+
					"the server then refuses every bootstrap with a 400 the log records as \"node "+
					"password not set\" -- MEASURED, in a container whose image had created the file "+
					"empty: /readyz stayed 503 for the whole 2-minute window with that one line as its "+
					"only account, the cluster sat in Initializing until the init timeout, and nothing "+
					"anywhere said the password was empty. Write a non-empty password into it (k3s's own "+
					"are 32 random bytes rendered as hex, 32 characters and a newline on this host), "+
					"owned by the user the server runs as", f.password),
			}
		}
		return Result{
			Label: "host.k3s-node-password",
			Kind:  Holds,
			Detail: fmt.Sprintf("%s is writable by this user (%04o) and carries %d bytes, so the "+
				"server has a password to answer its bootstrap with", f.password, mode, size),
		}
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Result{
			Label: "host.k3s-node-password",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s cannot be opened read-write by this user: %v. k3s's server "+
				"bootstraps this file even with the agent disabled, its path is not under --data-dir "+
				"and no flag moves it, and MEASURED: the server then logs \"Waiting to retrieve agent "+
				"configuration; server is not ready: open %s: permission denied\" for ever while "+
				"/readyz stays 503, so the cluster sits in Initializing until the init timeout and the "+
				"only trace of the cause is that one line. It is a host-global resource on the same "+
				"footing as 6444: this user has to own it, or %s has to point at one it owns",
				f.password, err, f.password, envNodePassword),
		}
	}
	dir := filepath.Dir(f.password)
	probe := filepath.Join(dir, ".check-host")
	fd, err = os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Result{
			Label: "host.k3s-node-password",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s does not exist yet and neither it nor %s can be created by this "+
				"user: %v. The file is created by the first k3s server that starts, so a host where "+
				"the directory is not writable is a host where the first server gets past its "+
				"bootstrap and then never reports ready, with a permission error about a path "+
				"nothing configured", f.password, dir, err),
		}
	}
	fd.Close()
	os.Remove(probe)
	return Result{
		Label: "host.k3s-node-password",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s does not exist yet and %s is writable by this user, so the first "+
			"k3s server to start can create it. The live tests skip without this and a skip is not "+
			"a pass: nothing else in this tree starts a server at all", f.password, dir),
	}
}

func selinuxResult(f facts) Result {
	state := procValue("/sys/fs/selinux/enforce")
	if state == "" {
		return Result{
			Label: "host.selinux",
			Kind:  Holds,
			Detail: "SELinux is not enforcing here: /sys/fs/selinux/enforce does not exist, so no " +
				"policy can deny the execution of a binary that was built in the tree this project " +
				"lives in. That incident -- a build artifact labelled user_tmp_t and refused its own " +
				"execution -- cannot present inside a container either, and the container is where " +
				"this line will usually read this way",
		}
	}
	if state == "0" {
		return Result{
			Label: "host.selinux",
			Kind:  Holds,
			Detail: "/sys/fs/selinux/enforce is 0: SELinux is present and permissive here, so a " +
				"policy may label a freshly built binary but it will not deny its execution",
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return Result{Label: "host.selinux", Kind: Missing,
			Detail: fmt.Sprintf("SELinux is enforcing and this process cannot name its own "+
				"executable to reproduce the build-a-binary-and-run-it shape: %v", err)}
	}
	dir, err := os.MkdirTemp(f.repo, ".check-host-exec-")
	if err != nil {
		return Result{
			Label: "host.selinux",
			Kind:  Missing,
			Detail: fmt.Sprintf("SELinux is enforcing and this check cannot make a temporary "+
				"directory under %s to build a binary in: %v. The incident this check exists for is a "+
				"binary BUILT IN THE REPO TREE that this user could not execute, and it presents as a "+
				"permission error somewhere unrelated -- so the tree the tests are built in has to be "+
				"writable by the user that builds and runs them, or the failure is read as a missing "+
				"file", f.repo, err),
		}
	}
	defer os.RemoveAll(dir)
	copyPath := filepath.Join(dir, "exec-probe")
	if err := copyExecutable(executable, copyPath); err != nil {
		return Result{Label: "host.selinux", Kind: Missing,
			Detail: fmt.Sprintf("SELinux is enforcing and %s could not be copied into %s: %v",
				executable, copyPath, err)}
	}
	out, err := exec.Command(copyPath, "-probe-exec").CombinedOutput()
	if err != nil {
		return Result{
			Label: "host.selinux",
			Kind:  Missing,
			Detail: fmt.Sprintf("SELinux is enforcing and this user cannot execute %s, a copy of "+
				"this binary placed in the repo tree: %v: %s. This is the user_tmp_t incident "+
				"exactly: a binary created in this tree is labelled for temporary space, the exec is "+
				"denied, and the error names the path rather than the policy, so it is read as a "+
				"missing file or a bad build. Its contexts: %s on the file, %s on the directory it "+
				"was created in. Fix the LABEL of the tree the tests are built in, not the test",
				copyPath, err, strings.TrimSpace(string(out)), selinuxContext(copyPath),
				selinuxContext(f.repo)),
		}
	}
	return Result{
		Label: "host.selinux",
		Kind:  Holds,
		Detail: fmt.Sprintf("SELinux is enforcing and this user executed a copy of this binary "+
			"placed in the repo tree (context %s, in %s). The check is by execution rather than by "+
			"reading a label: the incident this exists for passes every inspection and fails at exec",
			selinuxContext(copyPath), selinuxContext(dir)),
	}
}

func mib(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

func gib(n int64) string {
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func clientVersion(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Client Version:") {
			return strings.TrimSpace(line)
		}
	}
	return firstLine(s)
}

func procValue(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func orUnknown(value string) string {
	if value == "" {
		return "nothing at all (the file is not there)"
	}
	return value
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func readableFile(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%s is a directory", path)
	}
	buf := make([]byte, 1)
	if _, err := f.Read(buf); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func copyExecutable(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0o755)
}
