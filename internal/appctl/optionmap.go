package appctl

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/deploy"
)

const (
	OptionMapPath  = "apps/option-map.yaml"
	MapToOptions   = "options"
	MapToProfile   = "profile"
	MapToState     = "state"
	MapToListener  = "listener"
	MapToRemoved   = "removed"
	MapToSpec      = "spec."
	MapToNetwork   = "network."
	LegacyRemoval  = "legacy-transport"
	maxOptionValue = 4096
)

type OptionMap struct {
	Apps []AppOptions `json:"apps"`
}

type AppOptions struct {
	App     string          `json:"app"`
	Entry   string          `json:"entry"`
	Options []OptionMapping `json:"options"`
}

type OptionMapping struct {
	Name   string `json:"name"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

func LoadOptionMap() (OptionMap, error) {
	b, err := fs.ReadFile(deploy.FS, OptionMapPath)
	if err != nil {
		return OptionMap{}, fmt.Errorf("appctl: %w", err)
	}
	var m OptionMap
	if err := yaml.UnmarshalStrict(b, &m); err != nil {
		return OptionMap{}, fmt.Errorf("appctl: %s: %w", OptionMapPath, err)
	}
	for _, a := range m.Apps {
		for _, o := range a.Options {
			switch {
			case o.To == MapToOptions, o.To == MapToProfile, o.To == MapToState, o.To == MapToListener,
				strings.HasPrefix(o.To, MapToSpec), strings.HasPrefix(o.To, MapToNetwork):
			case o.To == MapToRemoved:
				if o.Reason != LegacyRemoval {
					return OptionMap{}, fmt.Errorf("appctl: %s: %s --%s is removed for %q; the only accepted removal is %s",
						OptionMapPath, a.App, o.Name, o.Reason, LegacyRemoval)
				}
			default:
				return OptionMap{}, fmt.Errorf("appctl: %s: %s --%s maps to %q", OptionMapPath, a.App, o.Name, o.To)
			}
		}
	}
	return m, nil
}

func (m OptionMap) app(name string) (AppOptions, bool) {
	for _, a := range m.Apps {
		if a.App == name {
			return a, true
		}
	}
	return AppOptions{}, false
}

func OptionArgs(app string, opts []v1alpha1.ApplicationOption) ([]string, error) {
	if len(opts) == 0 {
		return nil, nil
	}
	m, err := LoadOptionMap()
	if err != nil {
		return nil, err
	}
	a, ok := m.app(app)
	if !ok {
		return nil, fmt.Errorf("appctl: %s maps no option of %s", OptionMapPath, app)
	}
	var out []string
	for _, o := range opts {
		i := slices.IndexFunc(a.Options, func(x OptionMapping) bool { return x.Name == o.Name })
		if i < 0 {
			return nil, fmt.Errorf("appctl: %s has no option --%s (%s)", a.Entry, o.Name, OptionMapPath)
		}
		if to := a.Options[i].To; to != MapToOptions {
			return nil, fmt.Errorf("appctl: --%s of %s is not an options entry: it maps to %s", o.Name, a.Entry, to)
		}
		if len(o.Value) > maxOptionValue {
			return nil, fmt.Errorf("appctl: --%s of %s is longer than %d bytes", o.Name, a.Entry, maxOptionValue)
		}
		switch o.Value {
		case "true":
			out = append(out, "--"+o.Name)
		case "false":
		default:
			out = append(out, "--"+o.Name, o.Value)
		}
	}
	return out, nil
}
