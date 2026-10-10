package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type PreinstallFile struct {
	Path string `json:"path"`

	Mode string `json:"mode"`

	Source string `json:"source"`

	Content string `json:"content"`
}

type Preinstall struct {
	Packages []string `json:"packages"`

	Files []PreinstallFile `json:"files"`

	Runs []string `json:"runs"`
}

const preinstallResolv = "nameserver 1.1.1.1\nnameserver 9.9.9.9\n"

func LoadPreinstall(path string) (Preinstall, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Preinstall{}, fmt.Errorf("socialweb-nodeimage: reading the preinstall manifest at %s: "+
			"%w -- this file is what says which software is baked into the image so that the guest's "+
			"own cloud-init finds it already installed, and a manifest that cannot be read is one "+
			"silently not applied, which shows up later as a guest that installs nothing and reaches "+
			"no service", path, err)
	}
	var p Preinstall
	if err := json.Unmarshal(raw, &p); err != nil {
		return Preinstall{}, fmt.Errorf("socialweb-nodeimage: %s: %w", path, err)
	}
	if len(p.Packages) == 0 && len(p.Files) == 0 && len(p.Runs) == 0 {
		return Preinstall{}, fmt.Errorf("socialweb-nodeimage: the preinstall manifest at %s is empty. "+
			"An empty manifest is not a decision to bake nothing: it is a manifest whose lists were "+
			"not written, and the image it produces is one whose first boot reaches the network for "+
			"every package and every binary it needs, so it fails as a guest that never comes up "+
			"rather than as a manifest that says nothing", path)
	}
	for _, f := range p.Files {
		if !strings.HasPrefix(f.Path, "/") {
			return Preinstall{}, fmt.Errorf("socialweb-nodeimage: the preinstall manifest at %s names "+
				"the file %q, which is not an absolute path inside the guest. A relative path here "+
				"would be resolved against the build host's working directory and then written into "+
				"the image at a place nothing in the guest reads", path, f.Path)
		}
		if f.Content == "" && f.Source == "" {
			return Preinstall{}, fmt.Errorf("socialweb-nodeimage: the preinstall manifest at %s names "+
				"the file %q with neither content nor source, so there is nothing to write there", path, f.Path)
		}
		if f.Content != "" && f.Source != "" {
			return Preinstall{}, fmt.Errorf("socialweb-nodeimage: the preinstall manifest at %s names "+
				"the file %q with both content and source, and which of the two the image gets would "+
				"be decided by this code rather than by the manifest", path, f.Path)
		}
	}
	return p, nil
}

func applyPreinstall(cfg Config, preinstallPath string) error {
	if strings.TrimSpace(preinstallPath) == "" {
		return nil
	}
	p, err := LoadPreinstall(preinstallPath)
	if err != nil {
		return err
	}
	if err := writePreinstallFiles(cfg.StageDir, p.Files); err != nil {
		return err
	}
	if len(p.Packages) == 0 && len(p.Runs) == 0 {
		fmt.Printf("preinstall %s (files %d, no chroot needed)\n", preinstallPath, len(p.Files))
		return nil
	}
	if err := chrootPreinstall(cfg, preinstallPath, p); err != nil {
		return err
	}
	fmt.Printf("preinstall %s (packages %d, files %d, runs %d)\n",
		preinstallPath, len(p.Packages), len(p.Files), len(p.Runs))
	return nil
}

func writePreinstallFiles(stage string, files []PreinstallFile) error {
	for _, f := range files {
		target := filepath.Join(stage, filepath.Clean("/"+f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("socialweb-nodeimage: creating %s for the preinstalled file %s: %w",
				filepath.Dir(target), f.Path, err)
		}
		mode := os.FileMode(0o644)
		if f.Mode != "" {
			parsed, err := parseFileMode(f.Mode)
			if err != nil {
				return fmt.Errorf("socialweb-nodeimage: the preinstall manifest gives %s the mode %q: "+
					"%w -- a preinstalled binary the guest cannot execute is one whose absence only "+
					"shows up as a unit that fails to start inside a guest nobody is watching",
					f.Path, f.Mode, err)
			}
			mode = parsed
		}
		body := []byte(f.Content)
		if f.Source != "" {
			var err error
			body, err = os.ReadFile(f.Source)
			if err != nil {
				return fmt.Errorf("socialweb-nodeimage: reading %s, the source of the preinstalled file "+
					"%s: %w", f.Source, f.Path, err)
			}
		}
		if err := os.WriteFile(target, body, mode); err != nil {
			return fmt.Errorf("socialweb-nodeimage: writing the preinstalled file %s: %w", target, err)
		}
	}
	return nil
}

func parseFileMode(spelled string) (os.FileMode, error) {
	var n uint32
	if _, err := fmt.Sscanf(spelled, "%o", &n); err != nil {
		return 0, err
	}
	if n > 0o7777 {
		return 0, fmt.Errorf("%s is not a permission", spelled)
	}
	return os.FileMode(n), nil
}

func chrootPreinstall(cfg Config, preinstallPath string, p Preinstall) error {
	stage := cfg.StageDir
	mounted, err := mountChrootDevices(stage)
	if err != nil {
		unmountAll(mounted)
		return err
	}
	defer unmountAll(mounted)
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{
		{path: "tmp", mode: 0o1777},
		{path: "run", mode: 0o755},
		{path: "var/lib/apt/lists/partial", mode: 0o755},
		{path: "var/cache/apt/archives/partial", mode: 0o755},
		{path: "var/log", mode: 0o755},
		{path: "var/log/apt", mode: 0o755},
	} {
		full := filepath.Join(stage, dir.path)
		if err := os.MkdirAll(full, dir.mode); err != nil {
			return fmt.Errorf("socialweb-nodeimage: creating %s inside the staged root filesystem: %w "+
				"-- apt verifies the signature of every archive it reads, and the helper that reads the "+
				"keyring writes a temporary file: a staged tree with no /tmp is one where every "+
				"repository is reported as unsigned, which reads as a key problem rather than as a "+
				"missing directory", full, err)
		}
		if err := os.Chmod(full, dir.mode); err != nil {
			return fmt.Errorf("socialweb-nodeimage: setting %s to %v: %w", full, dir.mode, err)
		}
	}
	resolv := filepath.Join(stage, "etc/resolv.conf")
	previous, hadResolv := os.ReadFile(resolv)
	if err := os.MkdirAll(filepath.Join(stage, "etc"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(resolv, []byte(preinstallResolv), 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: writing %s for the preinstall: %w -- a chroot with no "+
			"resolver is one apt cannot reach an archive from", resolv, err)
	}
	defer func() {
		if hadResolv == nil {
			_ = os.WriteFile(resolv, previous, 0o644)
			return
		}
		_ = os.Remove(resolv)
	}()
	policy := filepath.Join(stage, "usr/sbin/policy-rc.d")
	if err := os.MkdirAll(filepath.Dir(policy), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(policy, []byte("#!/bin/sh\nexit 101\n"), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: writing %s: %w -- a package's post-install script "+
			"starts its services through invoke-rc.d, and there is no init in a staged root "+
			"filesystem for them to start under, so without this every package whose postinst "+
			"restarts something fails and dpkg leaves it half configured: the image then reads as "+
			"built while carrying, for instance, a cloud-init that is unpacked and never configured",
			policy, err)
	}
	steps := make([]string, 0, len(p.Packages)+len(p.Runs)+2)
	steps = append(steps,
		"export DEBIAN_FRONTEND=noninteractive",
		"export LC_ALL=C")
	if len(p.Packages) > 0 {
		quoted := make([]string, 0, len(p.Packages))
		for _, name := range p.Packages {
			quoted = append(quoted, shellQuote(name))
		}
		steps = append(steps,
			"apt-get update",
			"apt-get install -y --no-install-recommends "+
				"-o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef "+
				strings.Join(quoted, " "),
			"rm -rf /var/lib/apt/lists/*")
	}
	steps = append(steps, p.Runs...)
	if err := runStreaming("chroot", stage, "/bin/sh", "-c", strings.Join(steps, " && ")); err != nil {
		return fmt.Errorf("socialweb-nodeimage: running the preinstall from %s inside %s: %w -- the "+
			"preinstall is what the guest's own cloud-init will later find already done, so a failure "+
			"here is not a slow first boot: it is an image that reads as built and boots a guest whose "+
			"services never start", preinstallPath, stage, err)
	}
	return nil
}

func shellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

var chrootDeviceFiles = []string{"null", "zero", "random", "urandom", "tty"}

func mountChrootDevices(stage string) ([]string, error) {
	dev := filepath.Join(stage, "dev")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		return nil, fmt.Errorf("socialweb-nodeimage: creating %s: %w", dev, err)
	}
	mounted := make([]string, 0, len(chrootDeviceFiles)+1)
	pts := filepath.Join(dev, "pts")
	if err := os.MkdirAll(pts, 0o755); err != nil {
		return mounted, fmt.Errorf("socialweb-nodeimage: creating %s: %w", pts, err)
	}
	if err := run("mount", "-t", "devpts", "devpts", pts, "-o", "gid=5,mode=620,ptmxmode=000"); err != nil {
		return mounted, fmt.Errorf("socialweb-nodeimage: mounting a fresh devpts at %s for the "+
			"preinstall: %w -- dpkg writes its log through a pty, and a chroot with no pts is a "+
			"chroot where every install reports that it cannot write its log", pts, err)
	}
	mounted = append(mounted, pts)
	for _, name := range chrootDeviceFiles {
		target := filepath.Join(dev, name)
		if err := os.WriteFile(target, nil, 0o666); err != nil {
			return mounted, fmt.Errorf("socialweb-nodeimage: creating %s to bind /dev/%s over: %w",
				target, name, err)
		}
		if err := run("mount", "--bind", filepath.Join("/dev", name), target); err != nil {
			return mounted, fmt.Errorf("socialweb-nodeimage: binding /dev/%s over %s for the "+
				"preinstall: %w -- installing packages into a staged root filesystem needs the "+
				"device nodes its maintainer scripts read, and a build without the privilege to "+
				"bind them cannot bake anything. Run this build where it can mount, which is what "+
				"the container build does", name, target, err)
		}
		mounted = append(mounted, target)
	}
	return mounted, nil
}

func unmountAll(mounted []string) {
	for i := len(mounted) - 1; i >= 0; i-- {
		_ = run("umount", "-l", mounted[i])
	}
}

func unmountUnder(dir string) error {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading /proc/self/mountinfo to find anything "+
			"mounted under %s: %w -- a staged tree is cleared with a recursive remove, and a "+
			"recursive remove that walks a live bind mount deletes the host's files through it: "+
			"that is how /dev was emptied once already, so this refuses rather than clearing a "+
			"directory it cannot prove is unmounted", dir, err)
	}
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	var points []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		point := fields[4]
		if strings.HasPrefix(filepath.Clean(point)+string(filepath.Separator), prefix) {
			points = append(points, point)
		}
	}
	sort.Slice(points, func(i, j int) bool { return len(points[i]) > len(points[j]) })
	for _, point := range points {
		if err := run("umount", "-l", point); err != nil {
			return fmt.Errorf("socialweb-nodeimage: unmounting %s before clearing %s: %w -- "+
				"nothing under a staged tree may be mounted when it is removed", point, dir, err)
		}
	}
	return nil
}
