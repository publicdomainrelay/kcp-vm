package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const pastaWhoseVMMPowersOff = `#!/bin/sh
for last do :; done
dir=$(dirname "$last")
printf '{"pid":%d,"startNanos":1}\n' "$$" > "$dir/instance.json"
sleep 1
touch "$dir/vmm-exited"
`

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH: %v", tool, err)
		}
	}
}

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
	if err := os.WriteFile(rootfs, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(rootfs, 8<<20); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mkfs.ext4", "-q", "-F", rootfs).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}
}

func TestForegroundBootEndsWhenTheVMMExitsOnItsOwn(t *testing.T) {
	requireTools(t, "mkfs.ext4", "debugfs")
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
