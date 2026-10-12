package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vmruntime"
)

var vmOptions = append([]string{backendOpt, "instance", "state-dir", "asset-cache", "handle", "vm-runtime-dir", "guest-image-source", "vm-stop-grace"},
	devOnly("userns-stage-path", "pasta-path", "vmexec-path", "firecracker-path")...)

func (a *app) vmExe(vals clicfg.Values, executable, option string) (string, error) {
	if !assets.Release() {
		p := vals.String(option)
		if p == "" || !filepath.IsAbs(p) {
			return "", fmt.Errorf("%s must be an absolute path in this dev build (never PATH), got %q", option, p)
		}
		return p, nil
	}
	cache, err := a.assetCache(vals)
	if err != nil {
		return "", err
	}
	e, err := assets.Resolve(assets.Options{Cache: cache, Log: a.log}, executable)
	if err != nil {
		return "", err
	}
	return e.Path, nil
}

func (a *app) vmRuntime(ctx context.Context, vals clicfg.Values) (vmruntime.Runtime, hostsvc.Manager, error) {
	state := vals.String("state-dir")
	if state == "" || !filepath.IsAbs(state) {
		return nil, nil, fmt.Errorf("state-dir must be an absolute path: the VM work dirs, images and the slot lock live under it")
	}
	if vals.String("handle") == "" {
		return nil, nil, fmt.Errorf("handle is required: VM units are named by the handle of the bidder instance that owns them")
	}
	var exe vmruntime.Executables
	for _, x := range []struct {
		dst          *string
		name, option string
	}{
		{&exe.Stage, "swc-userns-stage", "userns-stage-path"},
		{&exe.Pasta, "pasta", "pasta-path"},
		{&exe.VMExec, "socialweb-vmexec", "vmexec-path"},
		{&exe.Firecracker, "firecracker", "firecracker-path"},
	} {
		p, err := a.vmExe(vals, x.name, x.option)
		if err != nil {
			return nil, nil, err
		}
		*x.dst = p
	}
	p, err := a.placement(ctx, vals)
	if err != nil {
		return nil, nil, err
	}
	grace, err := time.ParseDuration(vals.String("vm-stop-grace"))
	if err != nil {
		p.manager.Close()
		return nil, nil, err
	}
	rt, err := vmruntime.ForHost(vmruntime.Config{
		Manager: p.manager, StateDir: state, RuntimeDir: vals.String("vm-runtime-dir"), Exec: exe,
		ImageSource: vals.String("guest-image-source"), StopGrace: grace,
	})
	if err != nil {
		p.manager.Close()
		return nil, nil, err
	}
	return rt, p.manager, nil
}

func runVM(verb string) verbFunc {
	return func(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
		rt, m, err := a.vmRuntime(ctx, vals)
		if err != nil {
			return a.fail(exitRefused, "vm %s: %v", verb, err)
		}
		defer m.Close()
		handle := vals.String("handle")
		name := ""
		if verb != "start" && verb != "list" {
			if len(rest) != 1 {
				return a.fail(exitRefused, "vm %s: name exactly one VM", verb)
			}
			name = rest[0]
		}
		var out any
		switch verb {
		case "start":
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return a.fail(exitFailed, "vm start: reading the spec from stdin: %v", err)
			}
			var spec vmruntime.VMSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return a.fail(exitRefused, "vm start: the spec on stdin: %v", err)
			}
			spec.Handle = handle
			p, err := rt.Prepare(ctx, spec)
			if err != nil {
				return a.fail(exitFailed, "vm start: %v", err)
			}
			ref, err := rt.Start(ctx, p)
			if err != nil {
				return a.fail(exitFailed, "vm start: %v", err)
			}
			out = ref
		case "inspect":
			ref, err := rt.Load(handle, name)
			if os.IsNotExist(err) {
				out = vmruntime.Observation{Verdict: hostsvc.VerdictLost.String(), Reason: hostsvc.LostUnitGone, Detail: "no runtime record for " + name}
				break
			}
			if err != nil {
				return a.fail(exitFailed, "vm inspect: %v", err)
			}
			obs, err := rt.Inspect(ctx, ref)
			if err != nil {
				return a.fail(exitFailed, "vm inspect: %v", err)
			}
			out = obs
		case "stop":
			ref, err := rt.Load(handle, name)
			if os.IsNotExist(err) {
				ref = vmruntime.RuntimeRef{Handle: handle, Name: name, Unit: vmruntime.UnitName(handle, name)}
			} else if err != nil {
				return a.fail(exitFailed, "vm stop: %v", err)
			}
			res, err := rt.Stop(ctx, ref)
			if err != nil {
				return a.fail(exitFailed, "vm stop: %v", err)
			}
			out = res
		case "list":
			l, err := rt.List(ctx, handle)
			if err != nil {
				return a.fail(exitFailed, "vm list: %v", err)
			}
			out = l
		}
		a.printJSON(out)
		return exitOK
	}
}
