package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestimage"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestrootfs"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func digestOf(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

func writeGuestOutputs(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, guestimage.KernelName), []byte("ELF kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, guestimage.RootfsName), make([]byte, 3<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	report := guestrootfs.Report{
		TarSHA256: strings.Repeat("ab", 32),
		Bake: guestrootfs.Bake{
			Packages: []string{"cloud-init", "jq"},
			Files:    map[string]string{"/etc/cloud/cloud.cfg.d/99-kcp-datasource.cfg": digestOf([]byte("datasource_list: [ NoCloud, None ]\n"))},
			Assets:   map[string]string{"dumbpipe": strings.Repeat("cd", 32)},
		},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheGuestManifestRecordsEveryOutputAndBootsAsAFirecrackerImage(t *testing.T) {
	dir := t.TempDir()
	report := writeGuestOutputs(t, dir)
	if err := GuestManifest("firecracker-x86_64", dir, report); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, guestimage.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m guestimage.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Target != "firecracker-x86_64" || m.Hypervisor != "firecracker" || m.Arch != "x86_64" {
		t.Fatalf("manifest names %s/%s/%s", m.Target, m.Hypervisor, m.Arch)
	}
	if m.Kernel.SHA256 != digestOf([]byte("ELF kernel")) || m.Rootfs.SHA256 != digestOf(make([]byte, 3<<20)) {
		t.Fatal("the manifest's output hashes are not the outputs'")
	}
	if m.Inputs["kernelSource"].SHA256 == "" || m.Inputs["base"].SHA256 == "" || m.Inputs["kernelConfig"].SHA256 == "" {
		t.Fatalf("the manifest does not record its inputs: %+v", m.Inputs)
	}
	if len(m.Bake.Packages) != 2 || m.RootfsTarSHA256 == "" {
		t.Fatal("the manifest does not carry the bake record")
	}
	fc, err := nodefirecracker.LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := fc.CheckVMM("firecracker"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fc.BootArgs, " "), "root=/dev/vda") {
		t.Fatalf("socialweb-nodeboot would boot it with %v", fc.BootArgs)
	}
	again := filepath.Join(t.TempDir())
	writeGuestOutputs(t, again)
	if err := GuestManifest("firecracker-x86_64", again, report); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(filepath.Join(again, guestimage.ManifestName))
	if string(raw) != string(raw2) {
		t.Fatal("two manifests of the same outputs differ")
	}
}

func TestEnsureFetchesTheImageAGuestImageConfigNames(t *testing.T) {
	built := t.TempDir()
	report := writeGuestOutputs(t, built)
	if err := GuestManifest("firecracker-x86_64", built, report); err != nil {
		t.Fatal(err)
	}
	pin := guestimage.Pin{Target: "firecracker-x86_64", Hypervisor: "firecracker", Arch: "x86_64", Files: map[string]guestimage.FilePin{}}
	for _, name := range []string{guestimage.KernelName, guestimage.RootfsName, guestimage.ManifestName} {
		raw, err := os.ReadFile(filepath.Join(built, name))
		if err != nil {
			t.Fatal(err)
		}
		pin.Files[name] = guestimage.FilePin{SHA256: digestOf(raw), Bytes: int64(len(raw))}
	}
	root := t.TempDir()
	configPath := filepath.Join(root, "guest-image.json")
	cfg := map[string]any{"guest_image": map[string]any{"target": "firecracker-x86_64", "store": "store", "source": "file://" + built}}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	gc, ok, err := guestImageConfigAt(configPath)
	if err != nil || !ok {
		t.Fatalf("a guest_image config was not recognised: %v", err)
	}
	if gc.Store != filepath.Join(root, "store") {
		t.Fatalf("store %s is not resolved against the config's directory", gc.Store)
	}
	preinstall := filepath.Join(root, "preinstall.json")
	if err := os.WriteFile(preinstall, []byte(`{"packages":["jq"],"files":[{"path":"/etc/cloud/cloud.cfg.d/99-kcp-datasource.cfg","mode":"0644","content":"datasource_list: [ NoCloud, None ]\n"}],"runs":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ensureGuest(configPath, gc, pin, preinstall, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != statePresent || result.Fingerprint != pin.Files[guestimage.ManifestName].SHA256 ||
		result.RootfsMiB != 3 || result.Config != configPath || result.Initramfs != "" {
		t.Fatalf("ensure reported %+v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Dir, guestimage.RootfsName)); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureGuest(configPath, gc, pin, preinstall, true); err != nil {
		t.Fatalf("-present-only after a fetch: %v", err)
	}
	other := filepath.Join(root, "other-preinstall.json")
	if err := os.WriteFile(other, []byte(`{"packages":["jq","unzip"],"files":[],"runs":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureGuest(configPath, gc, pin, other, false); err == nil || !strings.Contains(err.Error(), "unzip") {
		t.Fatalf("a preinstall the image does not carry was accepted: %v", err)
	}
	empty := guestImageConfig{Target: gc.Target, Store: filepath.Join(root, "empty"), Source: gc.Source}
	if _, err := ensureGuest(configPath, empty, pin, "", true); err == nil {
		t.Fatal("-present-only answered present for an empty store")
	}
}
