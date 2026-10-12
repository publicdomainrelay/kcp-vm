package denoruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/provider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpapply"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/execrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/impl/policyclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

const (
	ManifestSet       = "denoruntime"
	RunnerTimeout     = time.Duration(-1)
	TSRootEnv         = "SOCIALWEB_TS_ROOT"
	TSKeyEnv          = "SOCIALWEB_TS_KEY"
	TSModeEnv         = "SOCIALWEB_TS_MODE"
	PolicyEngineEntry = "ts/policy-engine/lib/policy-engine-server-gha-lite"
	RetryAfter        = 2 * time.Second
	WatchListEnv      = "KUBE_FEATURE_WatchListClient"
	ReapGrace         = 15 * time.Second
)

type Config struct {
	StateDir     string
	Deno         string
	Kubeconfig   string
	Closure      tsbundle.Prepared
	Unit         string
	ApplyTimeout time.Duration
	OpenBao      OpenBao
	OpenBaoFrom  func(context.Context) (OpenBao, error)
	Log          *slog.Logger
}

type OpenBao struct {
	Address string
	Token   string
}

func RunsDir(stateDir string) string { return filepath.Join(stateDir, "denokcp", "runs") }

func (c Config) Check() error {
	for name, p := range map[string]string{"state dir": c.StateDir, "deno": c.Deno, "kcp kubeconfig": c.Kubeconfig, "closure root": c.Closure.Root} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("denoruntime: the %s %q is not an absolute path", name, p)
		}
	}
	if c.Closure.Key == "" || c.Closure.DenoDirs == "" {
		return errors.New("denoruntime: the closure is not prepared (no key or DENO_DIR root)")
	}
	return nil
}

func (c Config) ClosureEnv() []string {
	return []string{TSRootEnv + "=" + c.Closure.Root, TSKeyEnv + "=" + c.Closure.Key, TSModeEnv + "=" + string(c.Closure.Mode)}
}

func (c Config) PodOptions(trust func() []byte) execrunner.PodOptions {
	return execrunner.PodOptions{
		DenoBin:      c.Deno,
		RunsDir:      RunsDir(c.StateDir),
		NamespaceDir: filepath.Join(c.Closure.DenoDirs, c.Closure.Key),
		ExtraEnv:     c.ClosureEnv(),
		Timeout:      RunnerTimeout,
		TrustBundle:  trust,
		Unit:         c.Unit,
	}
}

func (c Config) EngineOptions() execrunner.EngineOptions {
	return execrunner.EngineOptions{
		DenoBin:   c.Deno,
		ServerDir: filepath.Join(c.Closure.Root, PolicyEngineEntry),
		RunsDir:   RunsDir(c.StateDir),
		ExtraEnv:  c.ClosureEnv(),
		Timeout:   RunnerTimeout,
		Unit:      c.Unit,
	}
}

func (c Config) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log.With("component", "denoruntime")
}

func applySet(ctx context.Context, c Config, ap *kcpapply.Applier) error {
	ix, err := manifests.Load()
	if err != nil {
		return err
	}
	set, ok := ix.Set(ManifestSet)
	if !ok {
		return fmt.Errorf("denoruntime: deploy/sets.yaml names no %s set", ManifestSet)
	}
	values, err := manifests.DecodeValues(set.Values, nil)
	if err != nil {
		return err
	}
	rep, err := ap.Apply(ctx, set, values)
	if err != nil {
		return err
	}
	c.log().Info("the deno runtime schemas and exports are applied", "set", ManifestSet, "changed", rep.Changed())
	return nil
}

func Run(ctx context.Context, c Config) error {
	if err := c.Check(); err != nil {
		return err
	}
	log := c.log()
	if c.OpenBao.Address == "" && c.OpenBaoFrom != nil {
		log.Info("the deno runtime waits for its application's OpenBao before it issues any DenoPod certificate")
		ob, err := c.OpenBaoFrom(ctx)
		if err != nil {
			return err
		}
		c.OpenBao = ob
	}
	restCfg, err := kcpserver.FrontRestConfig(c.Kubeconfig)
	if err != nil {
		return err
	}
	ap, err := kcpapply.New(restCfg, kcpapply.Options{WaitTimeout: c.ApplyTimeout, Log: log})
	if err != nil {
		return err
	}
	if err := applySet(ctx, c, ap); err != nil {
		return err
	}
	registry, err := provider.NewRegistry(provider.RegistryOptions{Host: restCfg.Host, RestConfig: restCfg})
	if err != nil {
		return err
	}
	var p *provider.Provider
	pods, err := execrunner.NewPod(c.PodOptions(func() []byte {
		if p == nil {
			return restCfg.CAData
		}
		return p.TrustBundle()
	}))
	if err != nil {
		return err
	}
	engines, err := execrunner.NewEngine(c.EngineOptions())
	if err != nil {
		return err
	}
	p, err = provider.New(provider.Options{
		Registry:       registry,
		PolicyClient:   policyclient.New(),
		Runtime:        registry,
		RestConfig:     restCfg,
		Minter:         registry,
		PodRunner:      pods,
		EngineRunner:   engines,
		Host:           restCfg.Host,
		WriteStatus:    true,
		RunsDir:        RunsDir(c.StateDir),
		OpenBaoAddress: c.OpenBao.Address,
		OpenBaoToken:   c.OpenBao.Token,
		Log:            log,
	})
	if err != nil {
		return err
	}
	defer p.Close()
	log.Info("the deno runtime runs in the core", "deno", c.Deno, "runsDir", RunsDir(c.StateDir), "closure", c.Closure.Key,
		"closureRoot", c.Closure.Root, "unit", c.Unit, "kcp", restCfg.Host, "openbao", c.OpenBao.Address)
	return p.Run(ctx)
}

func Supervise(ctx context.Context, c Config) {
	log := c.log()
	if reaped := execrunner.ReapRuns(RunsDir(c.StateDir), ReapGrace); len(reaped) > 0 {
		log.Warn("stopped the DenoPod processes a previous core of this instance left running, before the deno runtime starts its own",
			"runs", fmt.Sprint(reaped))
	}
	for {
		err := Run(ctx, c)
		if ctx.Err() != nil {
			return
		}
		log.Error("the deno runtime stopped; starting it again", "err", fmt.Sprint(err), "after", RetryAfter.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(RetryAfter):
		}
	}
}
