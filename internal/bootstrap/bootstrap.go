package bootstrap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpstore"
)

const (
	ProfileMinimal = "minimal"
	RoleKine       = "kine-a"
	RoleKcp        = "kcp-a"
	PortPoll       = 20 * time.Millisecond
	ReadyPoll      = 200 * time.Millisecond
)

var unbuiltProfiles = map[string]string{
	"quick":    "M2-BOOTSTRAP-LAYER",
	"bed":      "M2-BOOTSTRAP-LAYER and M2-KCP-STORE (kine on postgres)",
	"prodlike": "M2-BOOTSTRAP-LAYER and M2-KCP-STORE (kine on postgres)",
}

var ErrUnbuiltProfile = errors.New("bootstrap: profile not built yet")

func CheckProfile(p string) error {
	if p == ProfileMinimal {
		return nil
	}
	if item, ok := unbuiltProfiles[p]; ok {
		return fmt.Errorf("%w: --profile %s is built by %s; --profile minimal (kine on sqlite + one kcp slot) is", ErrUnbuiltProfile, p, item)
	}
	return fmt.Errorf("bootstrap: unknown profile %q: want minimal (built) or quick, bed, prodlike", p)
}

type Config struct {
	Layout       devinstance.Layout
	KcpPath      string
	KinePath     string
	ReadyTimeout time.Duration
	Log          *slog.Logger
}

type Order struct {
	Role string
	Unit string
}

func Table(l devinstance.Layout) []Order {
	return []Order{{RoleKine, l.KineUnit()}, {RoleKcp, l.KcpUnit()}}
}

func (c Config) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func RSSKiB(pid int) int64 {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return n
		}
	}
	return 0
}

func ensureUnit(ctx context.Context, m hostsvc.Manager, cfg Config, rec *devinstance.Record, role string, spec hostsvc.UnitSpec) (devinstance.Unit, bool, error) {
	log := cfg.log().With("role", role, "unit", spec.Name)
	if prev := rec.Unit(role); prev != nil && prev.Identity.Unit == spec.Name {
		obs, err := m.Check(ctx, prev.Identity)
		if err != nil {
			return devinstance.Unit{}, false, fmt.Errorf("bootstrap: checking %s: %w", spec.Name, err)
		}
		switch obs.Verdict {
		case hostsvc.VerdictRunning:
			u := *prev
			u.Adopted = true
			if port, err := hostsvc.ListenPort(u.Identity.PID); err == nil && port != u.Port {
				log.Warn("an adopted unit listens on another port than recorded", "recorded", u.Port, "now", port)
				u.Port = port
				u.Endpoint = endpoint(role, port)
			}
			log.Info("adopted", "pid", u.Identity.PID, "invocation", u.Identity.InvocationID, "port", u.Port)
			return u, false, nil
		case hostsvc.VerdictUnknown:
			return devinstance.Unit{}, false, fmt.Errorf("bootstrap: %s is Unknown (%s); never re-created while the manager is unreachable", spec.Name, obs.Detail)
		}
		log.Info("recorded unit lost; placing it again", "reason", string(obs.Reason), "detail", obs.Detail)
	}
	st, err := m.Start(ctx, spec)
	if err != nil {
		return devinstance.Unit{}, false, err
	}
	if st.Adopted && st.State != hostsvc.Active {
		log.Info("a loaded unit of this name is not active; resetting it", "state", string(st.State), "result", st.Result)
		if err := m.Stop(ctx, spec.Name); err != nil {
			return devinstance.Unit{}, false, err
		}
		if err := m.ResetFailed(ctx, spec.Name); err != nil {
			return devinstance.Unit{}, false, err
		}
		if st, err = m.Start(ctx, spec); err != nil {
			return devinstance.Unit{}, false, err
		}
	}
	id, err := m.Identify(ctx, spec.Name, 0)
	if err != nil {
		return devinstance.Unit{}, false, err
	}
	placedAt := st.ActiveSince
	if placedAt.IsZero() {
		placedAt = time.Now().UTC()
	}
	pctx, cancel := context.WithTimeout(ctx, cfg.ReadyTimeout)
	defer cancel()
	port, err := hostsvc.WaitListenPort(pctx, id.PID, PortPoll)
	if err != nil {
		return devinstance.Unit{}, false, fmt.Errorf("bootstrap: reading the bound port of %s (pid %d): %w", spec.Name, id.PID, err)
	}
	u := devinstance.Unit{Role: role, Unit: spec.Name, Identity: id, Port: port, Endpoint: endpoint(role, port),
		Adopted: st.Adopted, PlacedAt: placedAt, PortAt: time.Now().UTC()}
	log.Info("placed", "pid", id.PID, "invocation", id.InvocationID, "port", port, "adopted", st.Adopted,
		"portAfter", u.PortAt.Sub(placedAt).String())
	return u, !st.Adopted, nil
}

func endpoint(role string, port int) string {
	if role == RoleKine {
		return "http://127.0.0.1:" + strconv.Itoa(port)
	}
	return "https://127.0.0.1:" + strconv.Itoa(port)
}

func Ensure(ctx context.Context, m hostsvc.Manager, cfg Config, rec *devinstance.Record) error {
	l := cfg.Layout
	for _, d := range []string{l.KineDir, l.KcpRoot, l.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := kcpserver.WriteAuditPolicy(l.KcpRoot); err != nil {
		return err
	}
	kineSpec, err := kcpstore.KineSqliteSpec(kcpstore.KineConfig{Unit: l.KineUnit(), Slice: l.KcpSlice(), Exec: cfg.KinePath, Dir: l.KineDir})
	if err != nil {
		return err
	}
	kineSpec.Output = hostsvc.Output{StdoutFile: l.Logs + "/kine-a.log", StderrFile: l.Logs + "/kine-a.log"}
	kine, kinePlaced, err := ensureUnit(ctx, m, cfg, rec, RoleKine, kineSpec)
	if err != nil {
		return err
	}
	rec.SetUnit(kine)
	rec.KineURL = kine.Endpoint
	slotSpec, err := kcpserver.SlotSpec(kcpserver.SlotConfig{Unit: l.KcpUnit(), Slice: l.KcpSlice(), Exec: cfg.KcpPath,
		Root: l.KcpRoot, EtcdServers: kine.Endpoint, Wants: []string{l.KineUnit()}})
	if err != nil {
		return err
	}
	slotSpec.Output = hostsvc.Output{StdoutFile: l.Logs + "/kcp-a.log", StderrFile: l.Logs + "/kcp-a.log"}
	if kinePlaced {
		if prev := rec.Unit(RoleKcp); prev != nil && prev.Identity.Unit != "" {
			cfg.log().Info("kine was placed again on a new port; the slot that dialed the old one is restarted", "kine", kine.Endpoint)
			if err := m.Stop(ctx, slotSpec.Name); err != nil {
				return err
			}
			m.ResetFailed(ctx, slotSpec.Name)
		}
	}
	kcp, kcpPlaced, err := ensureUnit(ctx, m, cfg, rec, RoleKcp, slotSpec)
	if err != nil {
		return err
	}
	notBefore := time.Time{}
	if kcpPlaced {
		notBefore = kcp.PlacedAt
	}
	rctx, cancel := context.WithTimeout(ctx, cfg.ReadyTimeout)
	defer cancel()
	readyAt, err := kcpserver.WaitReady(rctx, kcp.Port, l.AdminKubeconfig(), notBefore, ReadyPoll)
	if err != nil {
		return fmt.Errorf("bootstrap: %s on 127.0.0.1:%d: %w", kcp.Unit, kcp.Port, err)
	}
	if kcpPlaced || kcp.ReadyAt.IsZero() {
		kcp.ReadyAt = readyAt
		kcp.StartedTo = readyAt.Sub(kcp.PlacedAt).Round(time.Millisecond).String()
	}
	kcp.RSSKiB = RSSKiB(kcp.Identity.PID)
	kine.RSSKiB = RSSKiB(kine.Identity.PID)
	rec.SetUnit(kine)
	rec.SetUnit(kcp)
	rec.KcpURL = kcp.Endpoint
	rec.AdminKubeconfig = l.AdminKubeconfig()
	rec.Ready = true
	rec.Updated = time.Now().UTC()
	cfg.log().Info("kcp ready", "port", kcp.Port, "startToReady", kcp.StartedTo, "kcpRSSKiB", kcp.RSSKiB, "kineRSSKiB", kine.RSSKiB)
	return nil
}
