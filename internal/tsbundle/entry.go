package tsbundle

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
)

var DefaultProbe = []string{"--help"}

type EntrySpec struct {
	Name          string `json:"name" yaml:"name"`
	Dir           string `json:"dir" yaml:"dir,omitempty"`
	denorun.Entry `yaml:",inline"`
	Probe         []string `json:"probe" yaml:"probe,omitempty"`
}

func (e EntrySpec) At(base string) (string, denorun.Entry) {
	abs := func(p string) string { return filepath.Join(base, filepath.FromSlash(p)) }
	en := denorun.Entry{Path: abs(e.Path), Unstable: e.Unstable}
	for _, p := range e.Preload {
		en.Preload = append(en.Preload, abs(p))
	}
	if e.Config != "" {
		en.Config = abs(e.Config)
	}
	return abs(e.Dir), en
}

func CheckEntries(entries []EntrySpec) error {
	if len(entries) == 0 {
		return errors.New("no entries")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Name == "" || seen[e.Name] {
			return fmt.Errorf("entry name %q is empty or repeated", e.Name)
		}
		seen[e.Name] = true
		for _, p := range append([]string{e.Path}, e.Preload...) {
			if err := CleanPath(p); err != nil {
				return fmt.Errorf("entry %s: %w", e.Name, err)
			}
		}
	}
	return nil
}

func CleanPath(p string) error {
	clean := path.Clean(p)
	if p == "" || path.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != p {
		return fmt.Errorf("path %q is not a clean path relative to the %s checkout", p, RepoName)
	}
	return nil
}

func StageUnit(p string) string {
	first, rest, ok := strings.Cut(p, "/")
	if !ok || first != VendorDir {
		return first
	}
	origin, _, _ := strings.Cut(rest, "/")
	return path.Join(first, origin)
}
