package versions

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var seamOwners = map[string]string{
	"darwin/arm64":  "plan 0012",
	"darwin/amd64":  "plan 0012",
	"windows/amd64": "plan 0011",
	"windows/arm64": "plan 0011",
}

func TestTheMatrixListsDarwinAndWindowsAsSeamsWithNoAssets(t *testing.T) {
	ps, err := Platforms()
	if err != nil {
		t.Fatal(err)
	}
	for plat, owner := range seamOwners {
		p, ok := ps[plat]
		if !ok {
			t.Errorf("%s is not in the platform matrix", plat)
			continue
		}
		if p.Status != PlatformSeam || p.Owner != owner || len(p.Assets) != 0 || len(p.Budget) != 0 {
			t.Errorf("%s = %+v, want a seam owned by %s with no assets and no budget lines", plat, p, owner)
		}
		if !strings.Contains(p.Note, "builds and vets") {
			t.Errorf("%s note %q must say the seam builds and vets", plat, p.Note)
		}
	}
	var release []string
	for plat, p := range ps {
		if p.Status == PlatformRelease {
			release = append(release, plat)
		} else if _, ok := seamOwners[plat]; !ok {
			t.Errorf("%s: status %q; only linux/amd64 and linux/arm64 are release platforms in this plan", plat, p.Status)
		}
	}
	sort.Strings(release)
	if strings.Join(release, ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("release platforms = %v, want linux/amd64 and linux/arm64", release)
	}
}

func TestEveryReleasePlatformPinsEachAssetAndBudgetsIt(t *testing.T) {
	ps, err := Platforms()
	if err != nil {
		t.Fatal(err)
	}
	all, err := Assets()
	if err != nil {
		t.Fatal(err)
	}
	for plat, p := range ps {
		if p.Status != PlatformRelease {
			continue
		}
		pins, err := PlatformPins(plat)
		if err != nil {
			t.Fatal(err)
		}
		for _, pin := range pins {
			if len(pin.TreeSHA256) != 64 || pin.RawBytes <= 0 || pin.ZstdBytes <= 0 {
				t.Errorf("%s %s: pin %+v is incomplete (run scripts/stage-assets.sh --print-pins)", plat, pin.Name, pin.AssetPlatform)
			}
			if line := p.Budget[pin.Name]; line < pin.ZstdBytes {
				t.Errorf("%s: budget line %s = %d B is below its measured %d B", plat, pin.Name, line, pin.ZstdBytes)
			}
		}
		for _, reserved := range []string{"core", "typescript-closure", "guest-images"} {
			if _, ok := p.Budget[reserved]; !ok {
				t.Errorf("%s: budget has no %q line", plat, reserved)
			}
		}
		if p.Budget["guest-images"] != 0 {
			t.Errorf("%s: guest images are fetched, never embedded; their line stays 0", plat)
		}
		for name := range p.Budget {
			if _, isAsset := all[name]; !isAsset && name != "core" && name != "typescript-closure" && name != "guest-images" && name != "download-assets" {
				t.Errorf("%s: budget line %q names no asset", plat, name)
			}
		}
	}
	for name, a := range all {
		if a.Pending != "" {
			if len(a.Platforms) != 0 {
				t.Errorf("%s is pending (%s) but carries platform pins", name, a.Pending)
			}
			continue
		}
		if len(a.Executables) == 0 || a.Version == "" || a.Source == "" || a.Verify == "" {
			t.Errorf("%s: an asset needs a version, a source, how it is verified and its executables", name)
		}
	}
}

func TestEveryEmbeddedInventoryEntryIsAnAssetExecutable(t *testing.T) {
	inv := loadHostInventory(t)
	all, err := Assets()
	if err != nil {
		t.Fatal(err)
	}
	carried := map[string]string{}
	for name, a := range all {
		for exe := range a.Executables {
			carried[exe] = name
		}
	}
	embedded := map[string]bool{}
	for _, e := range inv.Entries {
		if e.Kind != "executable" || !strings.HasPrefix(e.Fate, "embedded-") {
			continue
		}
		embedded[e.Name] = true
		if _, ok := carried[e.Name]; !ok {
			t.Errorf("host inventory marks %s %s, but no asset in versions.json carries it", e.Name, e.Fate)
		}
	}
	for exe, asset := range carried {
		if !embedded[exe] {
			t.Errorf("asset %s carries %s, which the host inventory does not mark embedded-*", asset, exe)
		}
	}
}

func buildRelease(t *testing.T, plat string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "socialweb-computer")
	cmd := exec.Command("go", "build", "-tags", "release", "-trimpath", "-o", out, "./cmd/socialweb-computer")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOOS="+strings.Split(plat, "/")[0], "GOARCH="+strings.Split(plat, "/")[1], "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build -tags release for %s: %v\n%s", plat, err, b)
	}
	return out
}

func TestTheReleaseBinaryFitsItsSizeBudget(t *testing.T) {
	ps, err := Platforms()
	if err != nil {
		t.Fatal(err)
	}
	plat := "linux/amd64"
	p := ps[plat]
	bin := buildRelease(t, plat)
	m, err := MeasureRelease(plat, bin, filepath.Join(repoRoot, "internal", "assets", "blobs", "linux_amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if over := CheckBudget(plat, p, m.Core, m.Assets); len(over) != 0 {
		t.Fatalf("over budget: %v (measured %+v)", over, m)
	}
	t.Logf("%s: core %d B, projected %d B of %d B budget, not staged %v", plat, m.Core, m.Projected, p.BudgetTotal(), m.NotStaged)
	one := Platform{Status: PlatformRelease, Budget: map[string]int64{"core": 1}}
	if over := CheckBudget(plat, one, m.Core, m.Assets); len(over) == 0 {
		t.Fatal("a 1-byte budget passed the gate; the gate does not gate")
	}
	empty := Platform{Status: PlatformRelease}
	if over := CheckBudget(plat, empty, m.Core, m.Assets); len(over) == 0 {
		t.Fatal("a release platform with no budget lines passed the gate")
	}
}
