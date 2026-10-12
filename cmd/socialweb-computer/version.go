package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
)

type versionInfo struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Revision string   `json:"revision,omitempty"`
	Time     string   `json:"time,omitempty"`
	Modified bool     `json:"modified"`
	Go       string   `json:"go"`
	GOOS     string   `json:"goos"`
	GOARCH   string   `json:"goarch"`
	Assets   []string `json:"assets"`
	AssetsBy string   `json:"assetsBy"`
}

func buildVersion(goos, goarch string) versionInfo {
	v := versionInfo{Name: serviceName, Version: "(devel)", Go: runtime.Version(), GOOS: goos, GOARCH: goarch,
		Assets: []string{}, AssetsBy: "M1-ASSETS (this dev build resolves kcp and kine from kcp-path and kine-path)"}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" {
			v.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				v.Revision = s.Value
			case "vcs.time":
				v.Time = s.Value
			case "vcs.modified":
				v.Modified = s.Value == "true"
			}
		}
	}
	return v
}

func runVersion(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	v := buildVersion(a.goos, a.goarch)
	if vals.Bool("json") {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(a.stdout, string(b))
		return exitOK
	}
	fmt.Fprintf(a.stdout, "%s %s %s %s %s/%s\n", v.Name, v.Version, v.Revision, v.Go, v.GOOS, v.GOARCH)
	return exitOK
}
