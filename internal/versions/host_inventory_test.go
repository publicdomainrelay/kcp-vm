package versions

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

const hostInventoryPath = "docs/host-inventory.yaml"

type inventoryEntry struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Fate     string   `json:"fate"`
	DrivenBy string   `json:"driven_by,omitempty"`
	Owner    string   `json:"owner"`
	Why      string   `json:"why"`
	From     []string `json:"from,omitempty"`
}

type indirectSite struct {
	Site     string   `json:"site"`
	Resolves []string `json:"resolves"`
}

type hostInventory struct {
	Note     string            `json:"note"`
	Fates    map[string]string `json:"fates"`
	Entries  []inventoryEntry  `json:"entries"`
	Indirect []indirectSite    `json:"indirect"`
}

func loadHostInventory(t *testing.T) hostInventory {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, hostInventoryPath))
	if err != nil {
		t.Fatalf("read %s: %v -- 0006 M1-HOST-INVENTORY: every executable the code execs needs a fate "+
			"recorded there", hostInventoryPath, err)
	}
	var inv hostInventory
	if err := yaml.UnmarshalStrict(raw, &inv); err != nil {
		t.Fatalf("parse %s: %v", hostInventoryPath, err)
	}
	return inv
}
