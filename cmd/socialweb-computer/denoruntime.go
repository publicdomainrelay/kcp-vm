package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devsup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/stack/denoruntime"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

func devOnly(names ...string) []string {
	if clicfg.ReleaseBuild() {
		return nil
	}
	return names
}

func (a *app) denoExe(ctx context.Context, vals clicfg.Values) (devinstance.Executable, error) {
	e, err := a.devExe(ctx, vals, denorun.Executable, "deno-path")
	if err != nil {
		return e, err
	}
	return e, denorun.CheckVersion(e.Path)
}

func closureSourceRoot(vals clicfg.Values, starts ...string) (string, error) {
	src, err := tsbundle.Select(vals, starts...)
	if err != nil {
		return "", fmt.Errorf("the TypeScript closure: %w", err)
	}
	if o, ok := src.(tsbundle.Checkout); ok {
		return o.Dir, nil
	}
	return "", nil
}

func (a *app) coreDenoRuntime(vals clicfg.Values, rec devinstance.Record, applyTimeout time.Duration) (denoruntime.Config, error) {
	deno := rec.Executables[denorun.Executable]
	if deno.Path == "" {
		return denoruntime.Config{}, errors.New("the instance records no deno; dev up records the pinned deno (deno-path in a dev build)")
	}
	src, err := tsbundle.Select(vals, rec.SourceRoot)
	if err != nil {
		return denoruntime.Config{}, fmt.Errorf("the TypeScript closure: %w", err)
	}
	prepared, err := src.Prepare(rec.Layout.StateDir)
	if err != nil {
		return denoruntime.Config{}, err
	}
	var bao denoruntime.OpenBao
	if rec.OpenBao != nil {
		token, err := os.ReadFile(rec.OpenBao.TokenFile)
		if err != nil {
			return denoruntime.Config{}, fmt.Errorf("the OpenBao token for %s: %w", rec.OpenBao.Address, err)
		}
		bao = denoruntime.OpenBao{Address: rec.OpenBao.Address, Token: strings.TrimSpace(string(token))}
	}
	unit, _ := a.env.LookupEnv(devsup.EnvUnit)
	return denoruntime.Config{StateDir: rec.Layout.StateDir, Deno: deno.Path, Kubeconfig: rec.Layout.FrontKubeconfig(),
		Closure: prepared, Unit: unit, ApplyTimeout: applyTimeout, OpenBao: bao, Log: a.log}, nil
}
