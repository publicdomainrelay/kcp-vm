package devcore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/appctl"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostprocess"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbaoctl"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/postgresctl"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/stack/denoruntime"
)

const CheckInterval = 5 * time.Second

type Config struct {
	Layout       devinstance.Layout
	ReadyTimeout time.Duration
	Log          *slog.Logger
	DenoRuntime  *denoruntime.Config
}

func tryLockRecord(path string) (*os.File, bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := hostos.Lock(f, hostos.LockExclusive|hostos.LockNonBlocking); err != nil {
		f.Close()
		return nil, false
	}
	return f, true
}

func lockRecord(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := hostos.Lock(f, hostos.LockExclusive|hostos.LockNonBlocking); err == nil {
			return f, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("devcore: %s stayed locked", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func Run(ctx context.Context, cfg Config, m hostsvc.Manager) error {
	l := cfg.Layout
	log := cfg.Log.With("component", "dev-core", "instance", l.ID)
	lf, err := os.OpenFile(l.CoreLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := hostos.Lock(lf, hostos.LockExclusive|hostos.LockNonBlocking); err != nil {
		return fmt.Errorf("devcore: another core holds %s: %w", l.CoreLock, err)
	}
	var rec devinstance.Record
	if err := devinstance.ReadJSON(l.Record, &rec); err != nil {
		return fmt.Errorf("devcore: reading %s: %w", l.Record, err)
	}
	var prev devinstance.Core
	devinstance.ReadJSON(l.CoreRecord, &prev)
	core := devinstance.Core{Generation: prev.Generation + 1, PID: os.Getpid(), InvocationID: os.Getenv("INVOCATION_ID"),
		StartedAt: time.Now().UTC()}
	bcfg := bootstrap.Config{Layout: l, KcpPath: rec.Executables["kcp"].Path, KinePath: rec.Executables["kine"].Path, PostgresRoot: rec.Executables["postgres"].Path,
		Exe: rec.Executables["socialweb-computer"].Path, ReadyTimeout: cfg.ReadyTimeout, Log: log}
	lk, err := lockRecord(l.Lock)
	if err != nil {
		return err
	}
	if err := devinstance.ReadJSON(l.Record, &rec); err != nil {
		lk.Close()
		return fmt.Errorf("devcore: reading %s: %w", l.Record, err)
	}
	err = ensure(ctx, m, bcfg, l, &rec, &core, log)
	lk.Close()
	if err != nil {
		return err
	}
	if cfg.DenoRuntime != nil {
		go denoruntime.Supervise(ctx, *cfg.DenoRuntime)
	}
	if len(rec.StandIns) == 0 && rec.AdminKubeconfig != "" {
		go hostprocess.Supervise(ctx, HostProcessConfig(bcfg, rec, m, log))
		go openbaoctl.Supervise(ctx, OpenBaoServerConfig(bcfg, rec, log))
		go postgresctl.Run(ctx, PostgresConfig(bcfg, rec, log))
		go appctl.Supervise(ctx, AppConfig(bcfg, rec, m, log))
	}
	if rec.Host && rec.AdminKubeconfig != "" {
		go devinstance.RunHost(ctx, devinstance.HostConfig{Kubeconfig: rec.AdminKubeconfig, Manager: m, Own: devinstance.Prefixes(l.ID), Log: log})
	}
	t := time.NewTicker(CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("core stopping", "generation", core.Generation)
			return nil
		case <-t.C:
		}
		lk, ok := tryLockRecord(l.Lock)
		if !ok {
			log.Info("a dev verb holds the instance; this check is skipped")
			continue
		}
		if err := devinstance.ReadJSON(l.Record, &rec); err != nil {
			lk.Close()
			log.Error("re-reading the instance record", "err", err.Error())
			continue
		}
		lost := false
		core.Verdicts = nil
		for _, u := range rec.Units {
			obs, restarted, err := bootstrap.Observe(ctx, m, u.Identity)
			if err != nil {
				log.Error("check", "unit", u.Unit, "err", err.Error())
				continue
			}
			if restarted {
				lost = true
				log.Warn("unit restarted in place by devsup; re-identifying it", "unit", u.Unit, "detail", obs.Detail)
				core.Verdicts = append(core.Verdicts, fmt.Sprintf("%s=Restarted", u.Unit))
				continue
			}
			core.Verdicts = append(core.Verdicts, fmt.Sprintf("%s=%s %s", u.Unit, obs.Verdict, obs.Reason))
			if obs.Verdict == hostsvc.VerdictLost {
				lost = true
				log.Warn("unit lost", "unit", u.Unit, "reason", string(obs.Reason), "detail", obs.Detail)
			}
		}
		core.LastCheck = time.Now().UTC()
		if lost {
			if err := ensure(ctx, m, bcfg, l, &rec, &core, log); err != nil {
				log.Error("re-placing the bootstrap units", "err", err.Error())
			}
			lk.Close()
			continue
		}
		lk.Close()
		devinstance.WriteJSON(l.CoreRecord, core)
	}
}

func ensure(ctx context.Context, m hostsvc.Manager, bcfg bootstrap.Config, l devinstance.Layout, rec *devinstance.Record,
	core *devinstance.Core, log *slog.Logger) error {
	if err := bootstrap.Ensure(ctx, m, bcfg, rec); err != nil {
		return err
	}
	core.Adoptions = nil
	for _, u := range rec.Units {
		core.Adoptions = append(core.Adoptions, devinstance.Adoption{Role: u.Role, Unit: u.Unit,
			InvocationID: u.Identity.InvocationID, PID: u.Identity.PID, Adopted: u.Adopted})
	}
	core.AdoptedAt = time.Now().UTC()
	core.LastCheck = core.AdoptedAt
	if err := devinstance.WriteJSON(l.Record, *rec); err != nil {
		return err
	}
	if err := devinstance.WriteJSON(l.CoreRecord, *core); err != nil {
		return err
	}
	log.Info("core holds the instance", "generation", core.Generation, "adoptions", core.Adoptions)
	return nil
}

var ErrNotReady = errors.New("devcore: the core did not record this generation in time")
