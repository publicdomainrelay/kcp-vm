package versions

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Platforms  map[string]Platform  `json:"platforms"`
	Assets     map[string]Asset     `json:"assets"`
}

const (
	PlatformRelease = "release"
	PlatformSeam    = "seam"
)

type Platform struct {
	Status string           `json:"status"`
	Owner  string           `json:"owner"`
	Note   string           `json:"note,omitempty"`
	Assets []string         `json:"assets"`
	Budget map[string]int64 `json:"budget,omitempty"`
}

func (p Platform) BudgetTotal() int64 {
	var t int64
	for _, v := range p.Budget {
		t += v
	}
	return t
}

type Asset struct {
	Version     string                   `json:"version"`
	Source      string                   `json:"source"`
	Verify      string                   `json:"verify"`
	Pending     string                   `json:"pending,omitempty"`
	Executables map[string]string        `json:"executables"`
	LibraryPath string                   `json:"libraryPath,omitempty"`
	Platforms   map[string]AssetPlatform `json:"platforms"`
}

type AssetPlatform struct {
	TreeSHA256 string `json:"treeSha256"`
	RawBytes   int64  `json:"rawBytes"`
	ZstdBytes  int64  `json:"zstdBytes"`
}

func load() (manifestShape, error) {
	var m manifestShape
	if err := json.Unmarshal(manifest, &m); err != nil {
		return m, fmt.Errorf("versions: manifest is not valid JSON: %w", err)
	}
	return m, nil
}

func All() (map[string]Component, error) {
	m, err := load()
	if err != nil {
		return nil, err
	}
	return m.Components, nil
}

func Platforms() (map[string]Platform, error) {
	m, err := load()
	if err != nil {
		return nil, err
	}
	return m.Platforms, nil
}

func Assets() (map[string]Asset, error) {
	m, err := load()
	if err != nil {
		return nil, err
	}
	return m.Assets, nil
}

type Pin struct {
	Name        string
	Platform    string
	Version     string
	Executables map[string]string
	LibraryPath string
	AssetPlatform
}

func PlatformPins(platform string) ([]Pin, error) {
	m, err := load()
	if err != nil {
		return nil, err
	}
	p, ok := m.Platforms[platform]
	if !ok {
		return nil, fmt.Errorf("versions: platform %s is not in the matrix", platform)
	}
	out := make([]Pin, 0, len(p.Assets))
	for _, name := range p.Assets {
		a, ok := m.Assets[name]
		if !ok {
			return nil, fmt.Errorf("versions: platform %s lists asset %q that has no pin", platform, name)
		}
		ap, ok := a.Platforms[platform]
		if !ok {
			return nil, fmt.Errorf("versions: asset %s has no pin for %s", name, platform)
		}
		out = append(out, Pin{Name: name, Platform: platform, Version: a.Version, Executables: a.Executables,
			LibraryPath: a.LibraryPath, AssetPlatform: ap})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type Measured struct {
	Platform   string           `json:"platform"`
	Binary     int64            `json:"binaryBytes"`
	Core       int64            `json:"coreBytes"`
	Assets     map[string]int64 `json:"assetBytes"`
	Projected  int64            `json:"projectedBytes"`
	NotStaged  []string         `json:"notStaged"`
	BudgetUsed int64            `json:"budgetBytes"`
}

func MeasureRelease(platform, binary, blobDir string) (Measured, error) {
	m := Measured{Platform: platform, Assets: map[string]int64{}, NotStaged: []string{}}
	st, err := os.Stat(binary)
	if err != nil {
		return m, err
	}
	m.Binary, m.Core = st.Size(), st.Size()
	pins, err := PlatformPins(platform)
	if err != nil {
		return m, err
	}
	for _, p := range pins {
		if bs, err := os.Stat(filepath.Join(blobDir, p.Name+".tar.zst")); err == nil && bs.Size() > 0 {
			m.Assets[p.Name] = bs.Size()
			m.Core -= bs.Size()
			continue
		}
		m.Assets[p.Name] = p.ZstdBytes
		m.NotStaged = append(m.NotStaged, p.Name)
	}
	m.Projected = m.Core
	for _, v := range m.Assets {
		m.Projected += v
	}
	return m, nil
}

func CheckBudget(platform string, p Platform, coreBytes int64, assetBytes map[string]int64) []string {
	var over []string
	if len(p.Budget) == 0 {
		return []string{fmt.Sprintf("%s: no budget lines recorded", platform)}
	}
	var total int64 = coreBytes
	if line, ok := p.Budget["core"]; !ok || coreBytes > line {
		over = append(over, fmt.Sprintf("%s: core %d B > budget line %d B", platform, coreBytes, line))
	}
	names := make([]string, 0, len(assetBytes))
	for n := range assetBytes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		total += assetBytes[n]
		if line, ok := p.Budget[n]; !ok || assetBytes[n] > line {
			over = append(over, fmt.Sprintf("%s: asset %s %d B > budget line %d B", platform, n, assetBytes[n], line))
		}
	}
	if b := p.BudgetTotal(); total > b {
		over = append(over, fmt.Sprintf("%s: release binary %d B > budget %d B", platform, total, b))
	}
	return over
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
