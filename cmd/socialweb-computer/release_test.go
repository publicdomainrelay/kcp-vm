package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

func TestAReleaseBinaryEitherCarriesEveryPinnedAssetOrRefusesToStart(t *testing.T) {
	plat := runtime.GOOS + "/" + runtime.GOARCH
	pins, err := versions.PlatformPins(plat)
	if err != nil {
		t.Skipf("%s is not in the platform matrix", plat)
	}
	bin := filepath.Join(t.TempDir(), "socialweb-computer")
	build := exec.Command("go", "build", "-tags", "release", "-trimpath", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -tags release: %v\n%s", err, b)
	}
	blobDir := filepath.Join("..", "..", "internal", "assets", "blobs", strings.ReplaceAll(plat, "/", "_"))
	var missing []string
	for _, p := range pins {
		if st, err := os.Stat(filepath.Join(blobDir, p.Name+".tar.zst")); err != nil || st.Size() == 0 {
			missing = append(missing, p.Name)
		}
	}
	cmd := exec.Command(bin, "version", "--json")
	cmd.Env = []string{"HOME=" + t.TempDir()}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		if code != exitRefused {
			t.Fatalf("assets %v are not staged, yet the release binary exited %d: %s", missing, code, out)
		}
		for _, m := range missing {
			if !strings.Contains(stderr.String(), m) {
				t.Errorf("the refusal does not name the missing asset %s: %s", m, stderr.String())
			}
		}
		return
	}
	if code != 0 {
		t.Fatalf("a fully staged release binary exited %d: %s", code, stderr.String())
	}
	var v versionInfo
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if !v.Release || len(v.Assets) != len(pins) {
		t.Fatalf("version --json lists %d assets (release=%v), want all %d", len(v.Assets), v.Release, len(pins))
	}
	for i, p := range pins {
		l := v.Assets[i]
		if l.Name != p.Name || l.Version != p.Version || l.SHA256 != p.TreeSHA256 || !l.Embedded {
			t.Errorf("version --json lists %+v, want %s %s %s embedded", l, p.Name, p.Version, p.TreeSHA256)
		}
	}
}
