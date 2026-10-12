package versions

import (
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
		if p.Status != PlatformSeam || p.Owner != owner || len(p.Assets) != 0 {
			t.Errorf("%s = %+v, want a seam owned by %s with no assets", plat, p, owner)
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
