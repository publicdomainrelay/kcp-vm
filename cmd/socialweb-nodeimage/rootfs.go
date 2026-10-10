package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

var guestUnits = map[string]string{
	"kcp-agent.service": `[Unit]
Description=kcp k3s agent in the guest
After=network-online.target systemd-modules-load.service
Wants=network-online.target systemd-modules-load.service

[Service]
Type=simple
ExecStart=` + nodefirecracker.GuestAgentPath + ` ` + nodefirecracker.AgentVerb + `
StandardOutput=journal+console
StandardError=journal+console
Restart=no
TimeoutStartSec=600

[Install]
WantedBy=multi-user.target
`,
}

var staleUnits = []string{
	"node-identity.service", "node-watch.service", "k3s-preflight.service",
}

const (
	modulesLoadDir  = "etc/modules-load.d"
	modulesLoadFile = "kcp.conf"
)

func renderGuestDocument(cfg Config) ([]byte, error) {
	doc := nodefirecracker.GuestDocument{
		Modules:   cfg.Modules,
		Interface: nodefirecracker.DefaultInterface,
		K3s: nodefirecracker.GuestK3s{
			Binary:  cfg.K3sBinary,
			DataDir: cfg.K3sDataDir,
		},
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("socialweb-nodeimage: rendering the guest document: %w", err)
	}
	return raw, nil
}

func buildRootfs(cfg Config, dst string, tars []imageTar) error {
	if err := unmountUnder(cfg.StageDir); err != nil {
		return err
	}
	if err := os.RemoveAll(cfg.StageDir); err != nil {
		return fmt.Errorf("socialweb-nodeimage: clearing the staging directory %s: %w -- the stage "+
			"is rebuilt from the base image every time, because a stage that keeps what the last "+
			"build put in it bakes one build's leftovers into the next image and the difference only "+
			"shows up as a guest that behaves unlike the image it came from", cfg.StageDir, err)
	}
	if err := os.MkdirAll(cfg.StageDir, 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", cfg.StageDir, err)
	}
	if err := run("unsquashfs", "-q", "-d", cfg.StageDir, cfg.BaseImage); err != nil {
		return fmt.Errorf("socialweb-nodeimage: unpacking the base image: %w", err)
	}
	version := filepath.Base(cfg.ModuleSource)
	modules := filepath.Join(cfg.StageDir, "lib/modules", version)
	if err := copyTree(cfg.ModuleSource, filepath.Join(cfg.StageDir, "lib/modules")); err != nil {
		return fmt.Errorf("socialweb-nodeimage: staging this host's module tree: %w -- the guest "+
			"boots this host's kernel, so the only modules that can load in it are the ones built "+
			"beside that kernel; any other tree is a guest whose k3s modules silently do not load",
			err)
	}
	for _, stale := range []string{"build", "source"} {
		if err := os.Remove(filepath.Join(modules, stale)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
	}
	configRaw, err := os.ReadFile(cfg.KernelConfig)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading %s: %w -- k3s check-config reads this "+
			"kernel's own config file to decide whether the kernel can run k3s at all, and a guest "+
			"without it reports every requirement as unmet", cfg.KernelConfig, err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.StageDir, "boot"), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StageDir, "boot", "config-"+version), configRaw, 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	kcpDir := filepath.Join(cfg.StageDir, "kcp")
	if err := os.MkdirAll(kcpDir, 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	if err := copyFile(cfg.AgentBinary, filepath.Join(kcpDir, "kcpagent"), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	doc, err := renderGuestDocument(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(kcpDir, nodefirecracker.GuestFileName), doc, 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	if err := installUnits(cfg); err != nil {
		return err
	}
	if err := stageModulesLoad(cfg); err != nil {
		return err
	}
	if err := stageK3s(cfg); err != nil {
		return err
	}
	if err := stageImageTars(cfg, tars); err != nil {
		return err
	}
	if err := disableSELinux(cfg.StageDir); err != nil {
		return err
	}
	if err := applyPreinstall(cfg, cfg.Preinstall); err != nil {
		return err
	}
	if err := verifyStage(cfg, tars); err != nil {
		return err
	}
	if err := verifyModuleNames(cfg); err != nil {
		return err
	}
	return layDownRootfs(cfg, dst)
}

func stageModulesLoad(cfg Config) error {
	dir := filepath.Join(cfg.StageDir, modulesLoadDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", dir, err)
	}
	body := strings.Join(cfg.Modules, "\n") + "\n"
	path := filepath.Join(dir, modulesLoadFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: writing %s: %w -- the guest's kernel modules are "+
			"listed here rather than loaded by a unit of ours, because systemd-modules-load.service is "+
			"already on every boot path and takes a list, and an agent that modprobed the same list "+
			"from a unit of its own put a process and a wait in front of every node start to do what "+
			"this file does declaratively", path, err)
	}
	return nil
}

func verifyModuleNames(cfg Config) error {
	index := filepath.Join(cfg.StageDir, "lib/modules", filepath.Base(cfg.ModuleSource))
	known := map[string]bool{}
	for _, name := range []string{"modules.dep", "modules.builtin"} {
		raw, err := os.ReadFile(filepath.Join(index, name))
		if err != nil {
			return fmt.Errorf("socialweb-nodeimage: reading %s: %w -- systemd-modules-load resolves "+
				"each name in %s through these two indexes, so a staged tree without one boots a guest "+
				"that loads nothing and says so only as a failed unit on a console nobody is reading",
				filepath.Join(index, name), err, filepath.Join(modulesLoadDir, modulesLoadFile))
		}
		for _, line := range strings.Split(string(raw), "\n") {
			entry := line
			if at := strings.Index(line, ":"); at >= 0 {
				entry = line[:at]
			}
			base := filepath.Base(strings.TrimSpace(entry))
			base = strings.TrimSuffix(base, ".xz")
			base = strings.TrimSuffix(base, ".ko")
			if base == "" || base == "." || base == "/" {
				continue
			}
			known[strings.ReplaceAll(base, "-", "_")] = true
		}
	}
	var absent []string
	for _, name := range cfg.Modules {
		if !known[strings.ReplaceAll(name, "-", "_")] {
			absent = append(absent, name)
		}
	}
	if len(absent) > 0 {
		return fmt.Errorf("socialweb-nodeimage: %s is in modules but in neither %s/modules.dep nor "+
			"%s/modules.builtin, so the guest cannot load %s and will not: a module that is neither a "+
			"file in this tree nor compiled into this kernel is a name that does nothing where it is "+
			"written, and the first iptables rule that needs it fails inside the guest long after the "+
			"line that named it was typed", strings.Join(absent, ", "), index, index,
			strings.Join(absent, ", "))
	}
	return nil
}

func verifyStage(cfg Config, tars []imageTar) error {
	var missing []string
	required := []struct{ what, path string }{
		{"the k3s binary the guest's agent execs", filepath.Join(cfg.StageDir, cfg.K3sBinary)},
		{"the agent the guest's units run", filepath.Join(cfg.StageDir, nodefirecracker.GuestAgentPath)},
		{"the guest document that names the modules and the k3s paths", filepath.Join(cfg.StageDir, nodefirecracker.GuestFilePath)},
		{"the modules the guest's systemd-modules-load reads", filepath.Join(cfg.StageDir, modulesLoadDir, modulesLoadFile)},
		{"the agent unit", filepath.Join(cfg.StageDir, "etc/systemd/system/kcp-agent.service")},
		{"the kernel config k3s check-config reads", filepath.Join(cfg.StageDir, "boot", "config-"+filepath.Base(cfg.ModuleSource))},
	}
	for _, t := range tars {
		required = append(required, struct{ what, path string }{
			"the seeded tarball for " + t.reference,
			filepath.Join(guestImagesDir(cfg), t.guestName),
		})
	}
	for _, req := range required {
		if _, err := os.Stat(req.path); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s: %v)", req.what, req.path, err))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("socialweb-nodeimage: the staged root filesystem is missing %s. This is "+
			"checked before the image is written because every one of these is only discovered "+
			"inside a booting guest: a missing k3s makes the guest's agent die at its first exec, "+
			"which arrives as a node that never joins minutes later and reads as a path or a "+
			"permission problem rather than as a file the build never staged, and a missing image "+
			"tarball arrives as a system pod that never leaves ContainerCreating while the guest "+
			"retries a pull its empty /etc/resolv.conf cannot resolve",
			strings.Join(missing, "; "))
	}
	return nil
}

func stageK3s(cfg Config) error {
	dst := filepath.Join(cfg.StageDir, cfg.K3sBinary)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", filepath.Dir(dst), err)
	}
	if err := copyFile(cfg.K3sBinary, dst, 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: staging k3s into the guest at %s: %w -- the guest "+
			"document names this path, and an image that does not carry the binary there boots a "+
			"guest whose agent fails at its first exec with \"no such file or directory\", which "+
			"reads as a permission or a path problem rather than as a file the build never copied",
			cfg.K3sBinary, err)
	}
	return nil
}

func installUnits(cfg Config) error {
	stage := cfg.StageDir
	units := filepath.Join(stage, "etc/systemd/system")
	wants := filepath.Join(units, "multi-user.target.wants")
	if err := os.MkdirAll(wants, 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	for _, stale := range staleUnits {
		if err := os.Remove(filepath.Join(wants, stale)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
	}
	for name, body := range guestUnits {
		if !cfg.IsK3sNode() {
			if err := os.Remove(filepath.Join(units, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("socialweb-nodeimage: %w", err)
			}
			if err := os.Remove(filepath.Join(wants, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("socialweb-nodeimage: %w", err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(units, name), []byte(body), 0o644); err != nil {
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
		link := filepath.Join(wants, name)
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
		if err := os.Symlink("/etc/systemd/system/"+name, link); err != nil {
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
	}
	return nil
}

func disableSELinux(stage string) error {
	path := filepath.Join(stage, "etc/selinux/config")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading %s: %w", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "SELINUX=") &&
			!strings.HasPrefix(strings.TrimSpace(line), "SELINUXTYPE=") {
			lines[i] = "SELINUX=disabled"
			found = true
		}
	}
	if !found {
		lines = append(lines, "SELINUX=disabled")
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: writing %s: %w", path, err)
	}
	return nil
}

func layDownRootfs(cfg Config, dst string) error {
	used, err := duMiB(cfg.StageDir)
	if err != nil {
		return err
	}
	size := used + cfg.RootfsHeadroomMiB
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", dst, err)
	}
	if err := f.Truncate(int64(size) << 20); err != nil {
		f.Close()
		return fmt.Errorf("socialweb-nodeimage: sizing %s to %d MiB: %w", dst, size, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	label := cfg.RootfsLabel
	if label == "" {
		label = "kcpguest"
	}
	if err := run("mkfs.ext4", "-q", "-F", "-d", cfg.StageDir, "-L", label, dst); err != nil {
		return fmt.Errorf("socialweb-nodeimage: laying the staged tree down as %s: %w -- "+
			"mkfs.ext4 -d populates an image file from a directory without mounting it, which is "+
			"what makes this build rootless", dst, err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		return err
	}
	fmt.Printf("rootfs %s (%d bytes, stage %d MiB, headroom %d MiB)\n",
		dst, st.Size(), used, cfg.RootfsHeadroomMiB)
	return nil
}

func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return run("cp", "-a", src, dst)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("socialweb-nodeimage: writing %s: %w", dst, err)
	}
	return out.Close()
}

func duMiB(dir string) (int, error) {
	out, err := exec.Command("du", "-sm", dir).Output()
	if err != nil {
		return 0, fmt.Errorf("socialweb-nodeimage: du -sm %s: %w", dir, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("socialweb-nodeimage: du -sm %s printed nothing", dir)
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("socialweb-nodeimage: du -sm %s printed %q: %w", dir, fields[0], err)
	}
	return n, nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err,
			strings.TrimSpace(buf.String()))
	}
	return nil
}

func runStreaming(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
