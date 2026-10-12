package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestimage"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestTwoBuildsOfAGuestImageAreByteIdenticalAndEqualTheirPins(t *testing.T) {
	if os.Getenv("SWC_GUEST_IMAGE_BUILD") != "1" {
		livegate.Require(t, "SWC_GUEST_IMAGE_BUILD=1 builds every guest image twice with "+
			"scripts/build-guest-image.sh (docker or podman, network for the pinned inputs, ~4 GB "+
			"under $HOME/.tmp) and compares the outputs byte for byte and against versions.json")
		return
	}
	targets := []string{"firecracker-x86_64", "firecracker-aarch64"}
	if only := os.Getenv("SWC_GUEST_IMAGE_TARGETS"); only != "" {
		targets = strings.Split(only, ",")
	}
	script := filepath.Join(repoRootDir(t), "scripts", "build-guest-image.sh")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	reproWork := filepath.Join(home, ".tmp", "guest-image-build-repro")
	for _, target := range targets {
		pin, err := guestimage.PinFor(target)
		if err != nil {
			t.Fatal(err)
		}
		var sums [2]map[string]string
		for i := range sums {
			out := t.TempDir()
			cmd := exec.Command(script, "--target", target, "--out", out, "--work", reproWork, "--clean", "--no-ccache")
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("build %d of %s: %v", i+1, target, err)
			}
			sums[i] = map[string]string{}
			for _, name := range []string{guestimage.KernelName, guestimage.RootfsName, guestimage.ManifestName} {
				rec, err := fileRecord(filepath.Join(out, target), name)
				if err != nil {
					t.Fatal(err)
				}
				sums[i][name] = rec.SHA256
			}
		}
		for name, first := range sums[0] {
			if sums[1][name] != first {
				t.Errorf("%s %s: build 1 %s, build 2 %s", target, name, first, sums[1][name])
			}
			if want := pin.Files[name].SHA256; want != first {
				t.Errorf("%s %s is %s and versions.json pins %s", target, name, first, want)
			}
		}
	}
}
