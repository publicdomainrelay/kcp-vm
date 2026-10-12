package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestimage"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestrootfs"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

const (
	guestVcpu   = 2
	guestMemMiB = 2048
)

type assetFlags map[string]string

func (a assetFlags) String() string {
	names := make([]string, 0, len(a))
	for k, v := range a {
		names = append(names, k+"="+v)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func (a assetFlags) Set(value string) error {
	name, path, ok := strings.Cut(value, "=")
	if !ok || name == "" || path == "" {
		return fmt.Errorf("-asset takes name=path, got %q", value)
	}
	a[name] = path
	return nil
}

func GuestRootfs(buildPath, basePath string, assets map[string]string, sourceDateEpoch int64, outPath, reportPath string) error {
	b, err := guestrootfs.LoadBuild(buildPath)
	if err != nil {
		return err
	}
	spec, err := b.Spec(guestrootfs.Options{SourceDateEpoch: sourceDateEpoch, Assets: assets})
	if err != nil {
		return err
	}
	base, err := os.Open(basePath)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: the base root tarball: %w", err)
	}
	defer base.Close()
	tmp := outPath + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	w := bufio.NewWriterSize(out, 1<<20)
	report, err := guestrootfs.Compose(bufio.NewReaderSize(base, 1<<20), spec, w)
	if err == nil {
		err = w.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	if err := os.WriteFile(reportPath, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	fmt.Printf("rootfs tar %s sha256 %s entries %d data %d bytes ssh host keys %d\n",
		outPath, report.TarSHA256, len(report.Entries), report.DataBytes, report.SSHHostKeys)
	return nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "internal", "versions", "versions.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("socialweb-nodeimage: no checkout of this module above the working " +
				"directory; the guest manifest records the sha256 of the kernel config it names")
		}
		dir = parent
	}
}

func fileRecord(dir, name string) (guestimage.FileRecord, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return guestimage.FileRecord{}, fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return guestimage.FileRecord{}, fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	return guestimage.FileRecord{File: name, SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n}, nil
}

func kernelRelease(version string, config []byte) string {
	s := bufio.NewScanner(strings.NewReader(string(config)))
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "CONFIG_LOCALVERSION="); ok {
			if local, err := strconv.Unquote(v); err == nil {
				return version + local
			}
		}
	}
	return version
}

func GuestManifest(target, dir, reportPath string) error {
	g, err := versions.GuestImages()
	if err != nil {
		return err
	}
	tg, ok := g.Targets[target]
	if !ok {
		return fmt.Errorf("socialweb-nodeimage: versions.json pins no guest image target %s", target)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: the rootfs report: %w", err)
	}
	var report guestrootfs.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %s: %w", reportPath, err)
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	config, err := os.ReadFile(filepath.Join(root, tg.KernelConfig))
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: the kernel config: %w", err)
	}
	inputs := map[string]guestimage.Input{
		"kernelSource": {URL: g.Kernel.Source.URL, SHA256: g.Kernel.Source.SHA256},
		"kernelConfig": {Path: tg.KernelConfig, SHA256: digest(config)},
		"base":         {URL: tg.Base.URL, SHA256: tg.Base.SHA256},
	}
	for name, rel := range map[string]string{"builder": g.Builder["dockerfile"], "rootfsBuild": "deploy/guest-image/rootfs.json"} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return fmt.Errorf("socialweb-nodeimage: %w", err)
		}
		inputs[name] = guestimage.Input{Path: rel, SHA256: digest(body)}
	}
	kernel, err := fileRecord(dir, guestimage.KernelName)
	if err != nil {
		return err
	}
	rootfs, err := fileRecord(dir, guestimage.RootfsName)
	if err != nil {
		return err
	}
	m := guestimage.Manifest{
		VMM:             tg.Hypervisor,
		BootArgs:        tg.BootArgs,
		Vcpu:            guestVcpu,
		MemMiB:          guestMemMiB,
		GuestInterface:  nodefirecracker.DefaultInterface,
		Target:          target,
		Hypervisor:      tg.Hypervisor,
		Arch:            tg.Arch,
		KernelVersion:   kernelRelease(g.Kernel.Version, config),
		Kernel:          kernel,
		Rootfs:          rootfs,
		RootfsLabel:     guestrootfs.RootfsLabel,
		RootfsTarSHA256: report.TarSHA256,
		SourceDateEpoch: tg.SourceDateEpoch,
		Bake:            report.Bake,
		Inputs:          inputs,
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	path := filepath.Join(dir, guestimage.ManifestName)
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	fmt.Printf("manifest %s\n", path)
	return nil
}

func digest(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

type guestImageConfig struct {
	Target string `json:"target"`
	Store  string `json:"store"`
	Source string `json:"source"`
}

func guestImageConfigAt(path string) (guestImageConfig, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return guestImageConfig{}, false, fmt.Errorf("socialweb-nodeimage: reading %s: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return guestImageConfig{}, false, fmt.Errorf("socialweb-nodeimage: %s: %w", path, err)
	}
	section, ok := top["guest_image"]
	if !ok {
		return guestImageConfig{}, false, nil
	}
	var gc guestImageConfig
	if err := json.Unmarshal(section, &gc); err != nil {
		return guestImageConfig{}, false, fmt.Errorf("socialweb-nodeimage: %s guest_image: %w", path, err)
	}
	if gc.Target == "" || gc.Store == "" || gc.Source == "" {
		return guestImageConfig{}, false, fmt.Errorf("socialweb-nodeimage: %s guest_image needs target, "+
			"store and source: the target names the pinned image, the store is where it is kept by "+
			"its sha256, and the source is the file:// or https:// directory it is fetched from", path)
	}
	base := filepath.Dir(path)
	if !filepath.IsAbs(gc.Store) {
		gc.Store = filepath.Join(base, gc.Store)
	}
	if !strings.Contains(gc.Source, "://") {
		dir := gc.Source
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		gc.Source = "file://" + dir
	}
	return gc, true, nil
}

func refuseGuestImageConfig(configPath, verb string) error {
	if configPath == "" {
		return nil
	}
	if _, ok, err := guestImageConfigAt(configPath); err == nil && ok {
		return fmt.Errorf("socialweb-nodeimage: %s names a pinned guest image, which %s does not "+
			"apply to: -ensure fetches and verifies it, and scripts/build-guest-image.sh builds it", configPath, verb)
	}
	return nil
}

func checkGuestPreinstall(dir, preinstallPath string) error {
	if strings.TrimSpace(preinstallPath) == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, guestimage.ManifestName))
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	var m guestimage.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %s: %w", dir, err)
	}
	pre, err := os.ReadFile(preinstallPath)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: the preinstall manifest: %w", err)
	}
	var p guestrootfs.Preinstall
	if err := json.Unmarshal(pre, &p); err != nil {
		return fmt.Errorf("socialweb-nodeimage: %s: %w", preinstallPath, err)
	}
	have := map[string]bool{}
	for _, name := range m.Bake.Packages {
		have[name] = true
	}
	var missing []string
	for _, name := range p.Packages {
		if !have[name] {
			missing = append(missing, "package "+name)
		}
	}
	for _, f := range p.Files {
		if f.Source != "" {
			continue
		}
		if m.Bake.Files[f.Path] != digest([]byte(f.Content)) {
			missing = append(missing, "file "+f.Path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("socialweb-nodeimage: the guest image in %s does not carry %s from the "+
			"preinstall manifest %s; the image is pinned, so the manifest has to match what it baked",
			dir, strings.Join(missing, ", "), preinstallPath)
	}
	return nil
}

func ensureGuest(configPath string, gc guestImageConfig, pin guestimage.Pin, preinstallPath string, presentOnly bool) (ensureResult, error) {
	store := guestimage.Store{Root: gc.Store}
	result := ensureResult{
		State:       statePresent,
		Fingerprint: pin.Files[guestimage.ManifestName].SHA256,
		Dir:         store.Dir(pin),
		Config:      configPath,
	}
	if presentOnly {
		if _, err := store.Verify(pin); err != nil {
			result.State = stateAbsent
			return result, fmt.Errorf("socialweb-nodeimage: -present-only: the store %s does not hold "+
				"the pinned %s image whole: %w", gc.Store, gc.Target, err)
		}
	} else {
		got, err := store.Ensure(context.Background(), pin, gc.Source)
		if err != nil {
			return result, err
		}
		result.Dir = got.Dir
	}
	if err := checkGuestPreinstall(result.Dir, preinstallPath); err != nil {
		return result, err
	}
	result.Kernel = filepath.Join(result.Dir, guestimage.KernelName)
	result.Rootfs = filepath.Join(result.Dir, guestimage.RootfsName)
	result.RootfsMiB = pin.Files[guestimage.RootfsName].Bytes >> 20
	if result.RootfsMiB == 0 {
		result.RootfsMiB = rootfsTemplateMiB(result.Dir)
	}
	return result, nil
}
