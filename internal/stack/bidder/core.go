package bidder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap/table"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/computevm"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devcore"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/install"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/stack/denoruntime"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

const (
	ExeCore      = "socialweb-computer"
	ExeKcp       = "kcp"
	ExeKine      = "kine"
	ExeDeno      = "deno"
	ExeBao       = "bao"
	ApplyTimeout = 3 * time.Minute
)

type CoreConfig struct {
	StateDir     string
	Prefix       string
	Handle       string
	Executables  map[string]devinstance.Executable
	Closure      tsbundle.Prepared
	Spec         v1alpha1.MarketBidderSpec
	VM           devinstance.VMRuntime
	ComputeVM    computevm.Config
	FrontAddress string
	ReadyTimeout time.Duration
	Unit         string
	Log          *slog.Logger
}

func NewLayout(state, prefix, handle string) devinstance.Layout {
	hid := install.HandleID(handle)
	dir := filepath.Join(state, "handles", hid)
	kcp := filepath.Join(dir, "kcp")
	run := filepath.Join(kcp, "run")
	return devinstance.Layout{ID: "bidder-" + hid, Short: hid, StateDir: dir, RuntimeDir: run,
		Socket: filepath.Join(run, "sup.sock"), SupState: filepath.Join(dir, "sup"),
		Record: filepath.Join(dir, "bidder.json"), CoreRecord: filepath.Join(dir, "core.json"),
		Lock: filepath.Join(kcp, "instance.lock"), CoreLock: filepath.Join(kcp, "core.lock"), Logs: filepath.Join(dir, "logs"),
		KineDir: filepath.Join(kcp, "kine-a"), KcpRoot: filepath.Join(kcp, "slot-a"), Prefix: prefix}
}

func (c CoreConfig) app() table.App {
	a := table.Bidder(c.Handle, install.HandleID(c.Handle))
	if c.Prefix != "" {
		a.Prefix = c.Prefix
	}
	return a
}

func (c CoreConfig) record(l devinstance.Layout) (devinstance.Record, error) {
	var rec devinstance.Record
	if err := devinstance.ReadJSON(l.Record, &rec); err != nil {
		rec = devinstance.Record{Created: time.Now().UTC()}
	}
	p := bootstrap.InstallParams(c.app(), c.StateDir)
	exes := map[string]devinstance.Executable{}
	for k, v := range c.Executables {
		exes[k] = v
	}
	if kcp, kine := c.Executables[ExeKcp].Path, c.Executables[ExeKine].Path; kcp != "" && kine != "" {
		if err := bootstrap.LinkAssets(p, kcp, kine, ""); err != nil {
			return rec, err
		}
	}
	for name, link := range map[string]string{ExeKcp: p.KcpPath, ExeKine: p.KinePath} {
		if _, err := os.Stat(link); err != nil {
			return rec, fmt.Errorf("bidder: %s is not linked at %s (install links it; a dev build names it with its -path option): %w", name, link, err)
		}
		e := exes[name]
		e.Path = link
		exes[name] = e
	}
	vm := c.VM
	rec.Layout, rec.Profile, rec.Backend, rec.Executables = l, bootstrap.ProfileQuick, hostsvc.BackendSystemdUser, exes
	rec.BaseWorkspace, rec.Host, rec.Parent, rec.StandIns, rec.OpenBao, rec.VM = "", false, nil, nil, nil, &vm
	return rec, nil
}

func RunCore(ctx context.Context, c CoreConfig, m hostsvc.Manager) error {
	if !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("bidder: state dir %q is not absolute", c.StateDir)
	}
	l := NewLayout(c.StateDir, c.Prefix, c.Handle)
	for _, d := range []string{l.StateDir, l.KcpDir(), l.RuntimeDir, l.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if n := len(filepath.Join(l.RuntimeDir, "kine-a.sock")); n > devinstance.MaxSocketPath {
		return fmt.Errorf("bidder: the runtime dir %s makes socket paths of %d bytes, over %d", l.RuntimeDir, n, devinstance.MaxSocketPath)
	}
	rec, err := c.record(l)
	if err != nil {
		return err
	}
	if err := devinstance.WriteJSON(l.Record, rec); err != nil {
		return err
	}
	app := c.app()
	log := c.Log.With("component", "run-bidder", "handle", c.Handle)
	log.Info("the bidder application instance starts: kcp store, kine, front, its slot, then its MarketBidder root object",
		"state", l.StateDir, "units", app.Names().UnitPrefix(), "profile", c.Spec.Profile)
	deno := denoruntime.Config{StateDir: l.StateDir, Deno: c.Executables[ExeDeno].Path, Kubeconfig: l.FrontKubeconfig(), Closure: c.Closure,
		Unit: c.Unit, ApplyTimeout: ApplyTimeout, Log: c.Log}
	cvm := c.ComputeVM
	return devcore.Run(ctx, devcore.Config{Layout: l, ReadyTimeout: c.ReadyTimeout, Log: c.Log, Application: &app,
		FrontAddress: c.FrontAddress, DenoRuntime: &deno, ComputeVM: &cvm, SignInGuard: CheckSignIn,
		Before: func(ctx context.Context, rec devinstance.Record) error {
			k, err := OpenKcp(rec.AdminKubeconfig)
			if err != nil {
				return err
			}
			go c.applyRoot(ctx, k, log)
			return nil
		}}, m)
}

func (c CoreConfig) applyRoot(ctx context.Context, k *Kcp, log *slog.Logger) {
	started := time.Now()
	for {
		err := k.ApplyRoot(ctx, c.Spec)
		if err == nil {
			log.Info("the MarketBidder root object is applied; the core renders its components", "profile", c.Spec.Profile,
				"afterControllerStart", time.Since(started).String())
			return
		}
		if time.Since(started) > ApplyTimeout {
			log.Error("applying the MarketBidder root object; still retrying", "err", err.Error())
			started = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(Poll):
		}
	}
}
