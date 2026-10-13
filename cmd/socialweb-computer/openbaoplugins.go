package main

import (
	"errors"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbaoctl"
)

const openBaoPluginDirOption = "openbao-plugin-dir"

func (a *app) openBaoPluginExes(vals clicfg.Values) (map[string]devinstance.Executable, error) {
	out := map[string]devinstance.Executable{}
	dir := vals.String(openBaoPluginDirOption)
	if !assets.Release() {
		if dir == "" {
			return out, nil
		}
		for _, name := range openbaoctl.PluginAssets {
			e, err := hashExe(openBaoPluginDirOption, filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			out[name] = e
		}
		return out, nil
	}
	if dir != "" {
		return nil, errors.New(openBaoPluginDirOption + " is a dev-build option; this release build places only its embedded OpenBao plugins")
	}
	cache, err := a.assetCache(vals)
	if err != nil {
		return nil, err
	}
	for _, name := range openbaoctl.PluginAssets {
		e, err := assets.Resolve(assets.Options{Cache: cache, Log: a.log}, name)
		if err != nil {
			return nil, err
		}
		out[name] = devinstance.Executable{Path: e.Path, Version: e.Version}
	}
	return out, nil
}
