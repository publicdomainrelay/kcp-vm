package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

type Artifacts struct {
	Kernel    string
	Initramfs string
	Rootfs    string
}

func Build(cfg Config) (Artifacts, error) {
	out := Artifacts{
		Kernel:    filepath.Join(cfg.OutDir, nodefirecracker.VMLinuxName),
		Initramfs: filepath.Join(cfg.OutDir, nodefirecracker.InitramfsName),
		Rootfs:    filepath.Join(cfg.OutDir, nodefirecracker.RootfsImageName),
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return out, fmt.Errorf("socialweb-nodeimage: creating %s: %w", cfg.OutDir, err)
	}
	if err := checkImagePins(cfg); err != nil {
		return out, err
	}
	if err := VerifyModules(cfg); err != nil {
		return out, err
	}
	if err := checkVMMTransport(cfg); err != nil {
		return out, err
	}
	tars, err := pullImageTars(cfg)
	if err != nil {
		return out, err
	}
	if err := extractKernel(cfg.KernelSource, out.Kernel); err != nil {
		return out, err
	}
	if err := buildInitramfs(cfg, out.Initramfs); err != nil {
		return out, err
	}
	if err := buildRootfs(cfg, out.Rootfs, tars); err != nil {
		return out, err
	}
	if err := writeManifest(cfg, out, tars); err != nil {
		return out, err
	}
	return out, nil
}

func writeManifest(cfg Config, out Artifacts, tars []imageTar) error {
	inputs, err := nodefirecracker.RecordInputs(cfg.Path, buildConfigOf(cfg))
	if err != nil {
		return err
	}
	manifest := nodefirecracker.Manifest{
		VMM:            cfg.VMMName(),
		BootArgs:       cfg.BootArgs,
		K3sBinary:      cfg.K3sBinary,
		K3sDataDir:     cfg.K3sDataDir,
		Modules:        cfg.Modules,
		Images:         manifestImages(tars),
		GuestInterface: nodefirecracker.DefaultInterface,
		Inputs:         &inputs,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: rendering the image manifest: %w", err)
	}
	path := filepath.Join(cfg.OutDir, nodefirecracker.ManifestName)
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: writing %s: %w -- the manifest is how the boot line "+
			"travels with the kernel it boots; a substrate that kept its own copy would drift from "+
			"the image the moment either changed", path, err)
	}
	fmt.Printf("manifest %s\n", path)
	return nil
}

func buildConfigOf(cfg Config) nodefirecracker.BuildConfig {
	return nodefirecracker.BuildConfig{
		VMM:               cfg.VMMName(),
		Modules:           cfg.Modules,
		InitramfsModules:  cfg.InitramfsModules,
		Images:            cfg.Images,
		KernelSource:      cfg.KernelSource,
		KernelConfig:      cfg.KernelConfig,
		BaseImage:         cfg.BaseImage,
		K3sBinary:         cfg.K3sBinary,
		AgentBinary:       cfg.AgentBinary,
		AgentPackage:      cfg.AgentPackage,
		AgentGoFlags:      cfg.AgentGoFlags,
		ModuleSource:      cfg.ModuleSource,
		BootArgs:          cfg.BootArgs,
		K3sDataDir:        cfg.K3sDataDir,
		RootfsLabel:       cfg.RootfsLabel,
		RootfsHeadroomMiB: cfg.RootfsHeadroomMiB,
	}
}

func buildAgent(cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(cfg.AgentBinary), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", filepath.Dir(cfg.AgentBinary), err)
	}
	args := append([]string{"build", "-trimpath"}, cfg.AgentGoFlags...)
	args = append(args, "-o", cfg.AgentBinary, cfg.AgentPackage)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: building the guest's agent (%s -> %s): %v: %s -- "+
			"the guest's half of the agent is a static build for the guest, and the image must carry "+
			"the binary built from the same source as the host's half, or the two sides disagree "+
			"about how a guest is talked to", cfg.AgentPackage, cfg.AgentBinary, err,
			strings.TrimSpace(string(raw)))
	}
	return nil
}

func extractKernel(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading the host kernel at %s: %w -- the guest "+
			"boots this host's own kernel, which is what makes the module tree beside it a match; "+
			"a kernel built elsewhere would need its own modules built with it", src, err)
	}
	at := bytes.Index(raw, zstdMagic)
	if at < 0 {
		return fmt.Errorf("socialweb-nodeimage: %s carries no zstd stream, so there is nothing to "+
			"extract. This host's vmlinuz is a self-extracting image whose payload begins at a "+
			"zstd frame, which is what a VMM can boot; a kernel compressed some other way "+
			"needs its own decompressor here rather than a guess", src)
	}
	tmp, err := os.CreateTemp("", "kcp-vmlinuz-")
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw[at:]); err != nil {
		tmp.Close()
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", dst, err)
	}
	cmd := exec.Command("zstd", "-dc", "-q", tmp.Name())
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := out.Close()
	if closeErr != nil {
		return fmt.Errorf("socialweb-nodeimage: writing %s: %w", dst, closeErr)
	}
	head := make([]byte, len(elfMagic))
	f, err := os.Open(dst)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: re-reading %s: %w", dst, err)
	}
	defer f.Close()
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading the head of %s (zstd said %v: %s): %w",
			dst, runErr, strings.TrimSpace(stderr.String()), err)
	}
	if !bytes.Equal(head, elfMagic) {
		return fmt.Errorf("socialweb-nodeimage: the kernel extracted to %s begins with %q and not "+
			"with an ELF header, so a VMM would refuse it at boot: %v: %s",
			dst, head, runErr, strings.TrimSpace(stderr.String()))
	}
	if runErr != nil {
		fmt.Printf("kernel %s: zstd reported %v after the frame it decompressed, which is the "+
			"trailing data of the self-extracting image rather than a truncated kernel; the "+
			"extracted file is an ELF and is used\n", dst, runErr)
	}
	st, err := os.Stat(dst)
	if err != nil {
		return err
	}
	fmt.Printf("kernel %s (%d bytes, payload at %d)\n", dst, st.Size(), at)
	return nil
}

func buildInitramfs(cfg Config, dst string) error {
	tree, err := os.MkdirTemp("", "kcp-initramfs-")
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	defer os.RemoveAll(tree)
	for _, d := range []string{"bin", "modules", "newroot", "proc", "sys", "dev"} {
		if err := os.MkdirAll(filepath.Join(tree, d), 0o755); err != nil {
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
	}
	if err := copyFile("/usr/bin/busybox", filepath.Join(tree, "bin/busybox"), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: staging busybox into the initramfs: %w -- busybox is "+
			"the insmod the guest's PID 1 uses to load the transport module of the VMM it is booted "+
			"by, and loading a module through the raw syscall instead was measured to fail with "+
			"EFAULT on this kernel while the same file loads through insmod", err)
	}
	if err := copyFile(cfg.AgentBinary, filepath.Join(tree, "init"), 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: staging the agent as the initramfs's /init: %w -- "+
			"the initramfs runs before any root filesystem exists, so this must be the static "+
			"cross-compiled build, not a host binary with an interpreter nothing can load yet", err)
	}
	for _, m := range cfg.InitramfsModules {
		if err := decompressModule(cfg.ModuleSource, m, filepath.Join(tree, "modules", filepath.Base(m)+".ko")); err != nil {
			return err
		}
	}
	doc, err := renderGuestDocument(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tree, nodefirecracker.GuestFileName), doc, 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	return packInitramfs(tree, dst)
}

func decompressModule(moduleSource, rel, dst string) error {
	src := filepath.Join(moduleSource, rel+".ko.xz")
	out, err := exec.Command("xz", "-dc", src).Output()
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: xz -dc %s: %w -- this host's modules are compressed "+
			"and the guest's kernel has no decompressor for them at the point it needs this one, "+
			"so it is stored uncompressed in the initramfs", src, err)
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	return nil
}

func packInitramfs(tree, dst string) error {
	list := filepath.Join(tree, "filelist")
	f, err := os.Create(list)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	names := []string{"init", nodefirecracker.GuestFileName, "bin", "bin/busybox", "modules", "newroot", "proc", "sys", "dev"}
	for _, n := range names {
		if _, err := fmt.Fprintln(f, n); err != nil {
			f.Close()
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(tree, "modules"))
	if err != nil {
		f.Close()
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	for _, e := range entries {
		if _, err := fmt.Fprintln(f, "modules/"+e.Name()); err != nil {
			f.Close()
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	in, err := os.Open(list)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	defer in.Close()
	os.Remove(dst)
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", dst, err)
	}
	cmd := exec.Command("cpio", "-o", "-H", "newc", "--quiet")
	cmd.Dir = tree
	cmd.Stdin = in
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := out.Close()
	if runErr != nil {
		return fmt.Errorf("socialweb-nodeimage: cpio: %v: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	if closeErr != nil {
		return fmt.Errorf("socialweb-nodeimage: writing %s: %w", dst, closeErr)
	}
	st, err := os.Stat(dst)
	if err != nil {
		return err
	}
	fmt.Printf("initramfs %s (%d bytes)\n", dst, st.Size())
	return nil
}
