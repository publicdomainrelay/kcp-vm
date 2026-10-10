package versions

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed versions.json
var manifest []byte

type Component struct {
	Version   string `json:"version"`
	How       string `json:"how"`
	Measured  string `json:"measured"`
	Invariant string `json:"invariant,omitempty"`
	Disputed  string `json:"disputed,omitempty"`

	Image string `json:"image,omitempty"`

	BundleSHA256 string `json:"bundleSha256,omitempty"`

	Resolved string `json:"resolved,omitempty"`
}

type manifestShape struct {
	Note       string               `json:"note"`
	Components map[string]Component `json:"components"`
}

func All() (map[string]Component, error) {
	var m manifestShape
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("versions: manifest is not valid JSON: %w", err)
	}
	return m.Components, nil
}

func Get(name string) (Component, error) {
	all, err := All()
	if err != nil {
		return Component{}, err
	}
	c, ok := all[name]
	if !ok {
		return Component{}, fmt.Errorf("versions: no component %q", name)
	}
	return c, nil
}

func Names() ([]string, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}
