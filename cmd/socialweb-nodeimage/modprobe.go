package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	kernelDefaultModprobe = "/sbin/modprobe"
	modprobeConfigKey     = "CONFIG_MODPROBE_PATH="
	stageLinkHops         = 40
)

var rootfsModprobes = []string{"/usr/sbin/modprobe", "/sbin/modprobe", "/usr/bin/modprobe", "/bin/modprobe"}

func kernelModprobePath(config []byte) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(config))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, modprobeConfigKey) {
			continue
		}
		raw := strings.TrimPrefix(line, modprobeConfigKey)
		path, err := strconv.Unquote(raw)
		if err != nil {
			path = raw
		}
		if path == "" || !filepath.IsAbs(path) {
			return "", fmt.Errorf("socialweb-nodeimage: the kernel config sets %s%s, which is not an absolute "+
				"path, so the kernel this image boots execs nothing it can find when it loads a module on "+
				"demand", modprobeConfigKey, raw)
		}
		return filepath.Clean(path), nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("socialweb-nodeimage: reading the kernel config: %w", err)
	}
	return kernelDefaultModprobe, nil
}

func walkInStage(stage, guestPath string) (string, os.FileInfo, bool) {
	parts := strings.Split(strings.Trim(filepath.Clean("/"+guestPath), "/"), "/")
	current := "/"
	hops := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(filepath.Join(stage, next))
		if err != nil {
			return "", nil, false
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		hops++
		if hops > stageLinkHops {
			return "", nil, false
		}
		target, err := os.Readlink(filepath.Join(stage, next))
		if err != nil {
			return "", nil, false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(current, target)
		}
		current = "/"
		parts = append(strings.Split(strings.Trim(filepath.Clean("/"+target), "/"), "/"), parts...)
	}
	info, err := os.Stat(filepath.Join(stage, current))
	if err != nil {
		return "", nil, false
	}
	return filepath.Join(stage, current), info, true
}

func resolveInStage(stage, guestPath string) (string, bool) {
	path, info, ok := walkInStage(stage, guestPath)
	if !ok || info.IsDir() {
		return "", false
	}
	return path, true
}

func resolveDirInStage(stage, guestDir string) (string, bool) {
	path, info, ok := walkInStage(stage, guestDir)
	if !ok || !info.IsDir() {
		return "", false
	}
	return path, true
}

func linkKernelModprobe(cfg Config) error {
	config, err := os.ReadFile(cfg.KernelConfig)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading %s: %w -- the kernel config names the modprobe the "+
			"kernel execs for every module it loads on demand", cfg.KernelConfig, err)
	}
	want, err := kernelModprobePath(config)
	if err != nil {
		return err
	}
	if _, ok := resolveInStage(cfg.StageDir, want); ok {
		return nil
	}
	own := ""
	for _, candidate := range rootfsModprobes {
		if candidate == want {
			continue
		}
		if _, ok := resolveInStage(cfg.StageDir, candidate); ok {
			own = candidate
			break
		}
	}
	if own == "" {
		return fmt.Errorf("socialweb-nodeimage: the kernel this image boots execs %s to load a module on "+
			"demand and the rootfs holds no modprobe at %s or at any of %s, so every netfilter expression "+
			"the guest's iptables reaches for that systemd-modules-load did not already load is refused "+
			"with ENOENT: kube-router's iptables-restore fails and tenant NetworkPolicies are not "+
			"enforced. Install kmod in the rootfs (the preinstall's packages)", want, want,
			strings.Join(rootfsModprobes, ", "))
	}
	parent, ok := resolveDirInStage(cfg.StageDir, filepath.Dir(want))
	if !ok {
		if err := os.MkdirAll(filepath.Join(cfg.StageDir, filepath.Dir(want)), 0o755); err != nil {
			return fmt.Errorf("socialweb-nodeimage: creating %s in the stage: %w", filepath.Dir(want), err)
		}
		parent = filepath.Join(cfg.StageDir, filepath.Dir(want))
	}
	link := filepath.Join(parent, filepath.Base(want))
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("socialweb-nodeimage: replacing the dangling %s in the stage: %w", want, err)
	}
	if err := os.Symlink(own, link); err != nil {
		return fmt.Errorf("socialweb-nodeimage: linking %s to %s in the stage: %w -- the kernel this image "+
			"boots was built for a userspace whose modprobe lives at %s and the rootfs is one whose "+
			"modprobe lives at %s, and without the link no module loads on demand in the guest",
			want, own, err, want, own)
	}
	if _, ok := resolveInStage(cfg.StageDir, want); !ok {
		return fmt.Errorf("socialweb-nodeimage: %s still does not resolve inside the stage after linking it to %s",
			want, own)
	}
	fmt.Printf("modprobe %s -> %s (the kernel's CONFIG_MODPROBE_PATH, linked to the rootfs's own)\n", want, own)
	return nil
}
