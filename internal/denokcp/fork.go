package denokcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const RecordPath = "deploy/denokcp/vendor.json"

var ForkRoots = []string{"internal/denokcp", "internal/kcplibs"}

type Upstream struct {
	Repository string `json:"repository"`
	Module     string `json:"module"`
	Commit     string `json:"commit"`
}

type Patch struct {
	Patch string `json:"patch"`
	From  string `json:"from"`
	Root  string `json:"root"`
}

type Package struct {
	Path     string   `json:"path"`
	From     string   `json:"from"`
	Upstream string   `json:"upstream"`
	Added    []string `json:"added,omitempty"`
}

type Fork struct {
	Upstreams map[string]Upstream `json:"upstreams"`
	Adapted   string              `json:"adapted"`
	Patches   []Patch             `json:"patches"`
	Local     []string            `json:"local"`
	Packages  []Package           `json:"packages"`
}

func LoadFork(repoRoot string) (Fork, error) {
	var f Fork
	raw, err := os.ReadFile(filepath.Join(repoRoot, RecordPath))
	if err != nil {
		return f, err
	}
	var outer struct {
		Fork json.RawMessage `json:"fork"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return f, fmt.Errorf("%s: %w", RecordPath, err)
	}
	if len(outer.Fork) == 0 {
		return f, fmt.Errorf("%s has no fork record", RecordPath)
	}
	dec := json.NewDecoder(bytes.NewReader(outer.Fork))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s fork: %w", RecordPath, err)
	}
	return f, nil
}
