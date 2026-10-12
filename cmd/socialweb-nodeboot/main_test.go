package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

const pastaWhoseVMMPowersOff = `#!/bin/sh
for last do :; done
dir=$(dirname "$last")
printf '{"pid":%d,"startNanos":1}\n' "$$" > "$dir/instance.json"
sleep 1
touch "$dir/vmm-exited"
`

func writeFakeImage(t *testing.T, imageDir string) {
	t.Helper()
	manifest := map[string]any{"vmm": "firecracker", "bootArgs": []string{"console=ttyS0"}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "image.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rootfs := filepath.Join(imageDir, "rootfs.ext4")
	if err := os.WriteFile(rootfs, templateRootfs(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func templateRootfs() []byte {
	return bytes.Repeat([]byte("the template root filesystem, copied as-is\n"), 1<<14)
}

func TestForegroundBootEndsWhenTheVMMExitsOnItsOwn(t *testing.T) {
	root := t.TempDir()
	imageDir := filepath.Join(root, "image")
	workDir := filepath.Join(root, "guests", "fc-test")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeImage(t, imageDir)

	pasta := filepath.Join(root, "pasta")
	if err := os.WriteFile(pasta, []byte(pastaWhoseVMMPowersOff), 0o755); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user-data.yaml")
	if err := os.WriteFile(userData, []byte("#cloud-config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{
		"name":           "fc-test",
		"image_dir":      imageDir,
		"work_dir":       workDir,
		"firecracker":    "/nonexistent/firecracker",
		"pasta":          pasta,
		"user_data_file": userData,
		"network": map[string]any{
			"guest_ip": "172.30.0.2", "prefix": 30, "gateway": "172.30.0.1", "pasta_ip": "172.30.0.0",
		},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "boot-spec.json")
	if err := os.WriteFile(specPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- Boot(specPath, filepath.Join(root, "result.json"), true) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Boot returned an error once the VMM exited: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Boot -foreground is still running 20s after the VMM it hosts exited, so the runner " +
			"container stays Up around a guest that no longer exists and nobody learns it died")
	}
	if _, err := os.Stat(filepath.Join(workDir, "vmm-exited")); err != nil {
		t.Fatalf("Boot returned before the VMM exited: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "result.json")); err != nil {
		t.Fatalf("Boot reported no result for the guest it started: %v", err)
	}
}

func TestTheSeedIsASeparateCidataDriveAndTheRootfsIsNotEdited(t *testing.T) {
	root := t.TempDir()
	tools := filepath.Join(root, "host-tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(root, "host-tool-called")
	for _, tool := range []string{"debugfs", "e2fsck", "resize2fs", "mkfs.ext4", "mkfs.vfat", "genisoimage"} {
		stub := "#!/bin/sh\necho " + tool + " >> " + called + "\nexit 1\n"
		if err := os.WriteFile(filepath.Join(tools, tool), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	imageDir := filepath.Join(root, "image")
	workDir := filepath.Join(root, "guests", "fc-seed")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeImage(t, imageDir)
	pasta := filepath.Join(root, "pasta")
	if err := os.WriteFile(pasta, []byte(pastaWhoseVMMPowersOff), 0o755); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user-data.yaml")
	if err := os.WriteFile(userData, []byte("#cloud-config\nruncmd: [[echo, seeded]]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const diskMiB = 64
	spec := map[string]any{
		"name":           "fc-seed",
		"image_dir":      imageDir,
		"work_dir":       workDir,
		"firecracker":    "/nonexistent/firecracker",
		"pasta":          pasta,
		"shell":          "/bin/sh",
		"user_data_file": userData,
		"disk_mib":       diskMiB,
		"network": map[string]any{
			"guest_ip": "172.30.0.2", "prefix": 30, "gateway": "172.30.0.1", "pasta_ip": "172.30.0.0",
		},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "boot-spec.json")
	if err := os.WriteFile(specPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Boot(specPath, filepath.Join(root, "result.json"), true); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if raw, err := os.ReadFile(called); err == nil {
		t.Fatalf("the boot ran host filesystem tools: %s", raw)
	}

	copyOf, err := os.ReadFile(filepath.Join(workDir, nodevm.RootfsName))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(copyOf)) != diskMiB<<20 {
		t.Fatalf("the rootfs copy is %d bytes, want the contract's %d MiB", len(copyOf), diskMiB)
	}
	template := templateRootfs()
	if !bytes.Equal(copyOf[:len(template)], template) || bytes.Count(copyOf[len(template):], []byte{0}) != len(copyOf)-len(template) {
		t.Fatal("the rootfs copy differs from the template beyond the zeros it was truncated up with")
	}

	paramsRaw, err := os.ReadFile(filepath.Join(workDir, "namespace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var params nodevm.NamespaceParams
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		t.Fatal(err)
	}
	if params.NodeDisk == "" || filepath.Dir(params.NodeDisk) != workDir {
		t.Fatalf("the VMM is given no seed drive in the work dir: %q", params.NodeDisk)
	}
	if params.Initramfs != "" {
		t.Fatalf("an image with no initramfs is booted with one: %q", params.Initramfs)
	}
	seed, err := os.ReadFile(params.NodeDisk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(seed, []byte("cidata")) || !bytes.Contains(seed, []byte("runcmd: [[echo, seeded]]")) ||
		!bytes.Contains(seed, []byte("instance-id: fc-seed")) || !bytes.Contains(seed, []byte("macaddress")) {
		t.Fatal("the seed drive does not carry the label, user-data, meta-data and network-config")
	}
	if blkid, err := exec.LookPath("/usr/sbin/blkid"); err == nil {
		out, _ := exec.Command(blkid, "-p", "-o", "export", params.NodeDisk).CombinedOutput()
		if !strings.Contains(string(out), "LABEL=cidata") {
			t.Fatalf("blkid does not read the seed as cidata: %s", out)
		}
	}
}
