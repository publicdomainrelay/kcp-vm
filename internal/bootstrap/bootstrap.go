package bootstrap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/apiserverfront"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpstore"
)

const (
	ProfileMinimal   = "minimal"
	RoleKine         = "kine-a"
	RoleFront        = "front"
	RoleKcp          = "kcp-a"
	PortPoll         = 20 * time.Millisecond
	ReadyPoll        = 200 * time.Millisecond
	FrontAddress     = "127.0.0.1:0"
	LeaseStaleAfter  = 6 * time.Second
	LeaseWatchWindow = 12 * time.Second
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
	Exe          string
	FrontAddress string
	ReadyTimeout time.Duration
	OnListen     func(name string, addr net.Addr)
	Log          *slog.Logger
}

type Order struct {
	Role string
	Unit string
}

func SlotRole(slot string) string { return "kcp-" + slot }

func Table(l devinstance.Layout) []Order {
	return []Order{{RoleKine, l.KineUnit()}, {RoleFront, l.FrontUnit()}, {RoleKcp, l.SlotUnit(kcpserver.SlotA)}}
}

func (c Config) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c Config) frontAddress() string {
	if c.FrontAddress != "" {
		return c.FrontAddress
	}
	return FrontAddress
}

func (c Config) onListen(name string, port int) {
	if c.OnListen != nil {
		c.OnListen(name, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	}
}

func (c Config) frontSpec() (hostsvc.UnitSpec, error) {
	l := c.Layout
	return kcpserver.FrontSpec(kcpserver.FrontConfig{Unit: l.FrontUnit(), Slice: l.KcpSlice(), Exe: c.Exe, Owner: l.ID,
		Address: c.frontAddress(), Control: l.FrontControl(), State: l.FrontState(), Log: filepath.Join(l.Logs, "front.log")})
}

func (c Config) slotSpec(slot, kineURL, frontURL string) (hostsvc.UnitSpec, string, error) {
	l := c.Layout
	root := l.SlotRoot(slot)
	spec, err := kcpserver.SlotSpec(kcpserver.SlotConfig{Unit: l.SlotUnit(slot), Slice: l.KcpSlice(), Exec: c.KcpPath,
		Root: root, EtcdServers: kineURL, FrontURL: frontURL, Shape: kcpserver.ShapeBlueGreen,
		Wants: []string{l.KineUnit()}})
	if err != nil {
		return hostsvc.UnitSpec{}, "", err
	}
	spec.After = append(spec.After, l.FrontUnit())
	log := filepath.Join(l.Logs, "kcp-"+slot+".log")
	spec.Output = hostsvc.Output{StdoutFile: log, StderrFile: log}
	return spec, root, nil
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

func ensureUnit(ctx context.Context, m hostsvc.Manager, cfg Config, rec *devinstance.Record, role string, spec hostsvc.UnitSpec,
	beforeStart func() error) (devinstance.Unit, bool, error) {
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
	if beforeStart != nil {
		if st, err := m.Inspect(ctx, spec.Name); err != nil || st.State != hostsvc.Active {
			if err := beforeStart(); err != nil {
				return devinstance.Unit{}, false, err
			}
		}
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
		if beforeStart != nil {
			if err := beforeStart(); err != nil {
				return devinstance.Unit{}, false, err
			}
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

func readSlots(l devinstance.Layout) kcpserver.Slots {
	s, err := kcpserver.ReadSlots(l.SlotsFile())
	if err != nil || s.Active == "" {
		return kcpserver.Slots{Shape: kcpserver.ShapeBlueGreen, Active: kcpserver.SlotA, Slots: map[string]kcpserver.SlotRecord{}}
	}
	return s
}

func stopRecordedSlots(ctx context.Context, m hostsvc.Manager, cfg Config, rec *devinstance.Record, why string) error {
	for _, slot := range kcpserver.SlotNames(kcpserver.ShapeBlueGreen) {
		prev := rec.Unit(SlotRole(slot))
		if prev == nil || prev.Identity.Unit == "" {
			continue
		}
		cfg.log().Info(why+"; the slot that dialed the old one is restarted", "slot", slot)
		if err := m.Stop(ctx, prev.Unit); err != nil {
			return err
		}
		m.ResetFailed(ctx, prev.Unit)
	}
	return nil
}

func HandoverStaleLease(ctx context.Context, admin *kcpserver.HTTPAdmin, log *slog.Logger) (string, error) {
	first, err := admin.Lease(ctx)
	if err != nil || !first.Found {
		return "absent", err
	}
	deadline := time.Now().Add(LeaseWatchWindow)
	for {
		l, err := admin.Lease(ctx)
		if err != nil {
			return "", err
		}
		if !l.Found || l.Holder != first.Holder || l.RenewTime.After(first.RenewTime) {
			return "live", nil
		}
		if time.Since(l.RenewTime) > LeaseStaleAfter {
			log.Info("the controller lease is held by a gone process; deleting it", "holder", l.Holder,
				"renewed", l.RenewTime.UTC().Format(time.RFC3339Nano))
			return "deleted", admin.DeleteLease(ctx)
		}
		if time.Now().After(deadline) {
			return "unchanged", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(ReadyPoll):
		}
	}
}

func Ensure(ctx context.Context, m hostsvc.Manager, cfg Config, rec *devinstance.Record) error {
	l := cfg.Layout
	log := cfg.log()
	for _, d := range []string{l.KineDir, l.KcpDir(), l.Logs, l.RuntimeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	kineSpec, err := kcpstore.KineSqliteSpec(kcpstore.KineConfig{Unit: l.KineUnit(), Slice: l.KcpSlice(), Exec: cfg.KinePath, Dir: l.KineDir})
	if err != nil {
		return err
	}
	kineSpec.Output = hostsvc.Output{StdoutFile: l.Logs + "/kine-a.log", StderrFile: l.Logs + "/kine-a.log"}
	kine, kinePlaced, err := ensureUnit(ctx, m, cfg, rec, RoleKine, kineSpec, nil)
	if err != nil {
		return err
	}
	cfg.onListen(RoleKine, kine.Port)
	rec.SetUnit(kine)
	rec.KineURL = kine.Endpoint
	frontSpec, err := cfg.frontSpec()
	if err != nil {
		return err
	}
	front, frontPlaced, err := ensureUnit(ctx, m, cfg, rec, RoleFront, frontSpec, nil)
	if err != nil {
		return err
	}
	cfg.onListen(RoleFront, front.Port)
	rec.SetUnit(front)
	frontURL := kcpserver.FrontURL(front.Port)
	slots := readSlots(l)
	if kinePlaced || frontPlaced {
		why := "kine was placed again on a new port"
		if frontPlaced {
			why = "the front was placed again on a new port"
		}
		if err := stopRecordedSlots(ctx, m, cfg, rec, why); err != nil {
			return err
		}
	}
	active := slots.Active
	role := SlotRole(active)
	slotSpec, root, err := cfg.slotSpec(active, kine.Endpoint, frontURL)
	if err != nil {
		return err
	}
	if err := kcpserver.WriteAuditPolicy(root); err != nil {
		return err
	}
	seeded := false
	seed := func() error {
		if !kcpserver.HasShared(l.SharedDir()) {
			return nil
		}
		seeded = true
		return kcpserver.SeedSlot(l.SharedDir(), root)
	}
	prevInvocation := ""
	if prev := rec.Unit(role); prev != nil {
		prevInvocation = prev.Identity.InvocationID
	}
	kcp, kcpPlaced, err := ensureUnit(ctx, m, cfg, rec, role, slotSpec, seed)
	if err != nil {
		return err
	}
	cfg.onListen(role, kcp.Port)
	rctx, cancel := context.WithTimeout(ctx, cfg.ReadyTimeout)
	defer cancel()
	var readyAt time.Time
	if kcpserver.HasShared(l.SharedDir()) {
		a, err := kcpserver.ReadSharedAdmin(l.SharedDir())
		if err != nil {
			return err
		}
		admin, err := kcpserver.NewHTTPAdmin(a, frontURL)
		if err != nil {
			return err
		}
		if err := kcpserver.WaitOwnPortReady(rctx, admin, kcp.Port, ReadyPoll); err != nil {
			return fmt.Errorf("bootstrap: %s on 127.0.0.1:%d: %w", kcp.Unit, kcp.Port, err)
		}
		readyAt = time.Now().UTC()
	} else {
		notBefore := time.Time{}
		if kcpPlaced {
			notBefore = kcp.PlacedAt
		}
		readyAt, err = kcpserver.WaitReady(rctx, kcp.Port, filepath.Join(root, "admin.kubeconfig"), notBefore, ReadyPoll)
		if err != nil {
			return fmt.Errorf("bootstrap: %s on 127.0.0.1:%d: %w", kcp.Unit, kcp.Port, err)
		}
		if err := kcpserver.SaveShared(root, l.SharedDir()); err != nil {
			return err
		}
		log.Info("shared root material saved", "dir", l.SharedDir(), "from", root)
	}
	if kcpPlaced || kcp.ReadyAt.IsZero() {
		kcp.ReadyAt = readyAt
		kcp.StartedTo = readyAt.Sub(kcp.PlacedAt).Round(time.Millisecond).String()
	}
	a, err := kcpserver.ReadSharedAdmin(l.SharedDir())
	if err != nil {
		return err
	}
	if err := kcpserver.WriteFrontKubeconfig(l.FrontKubeconfig(), frontURL, a); err != nil {
		return err
	}
	slots.Shape = kcpserver.ShapeBlueGreen
	slots.Active = active
	slots.KineURL = kine.Endpoint
	slots.Front = kcpserver.FrontRecord{Unit: front.Unit, Address: "127.0.0.1:" + strconv.Itoa(front.Port), Port: front.Port,
		Control: l.FrontControl(), PID: front.Identity.PID}
	slots.Slots[active] = kcpserver.SlotRecord{Unit: kcp.Unit, Root: root, Port: kcp.Port, PID: kcp.Identity.PID,
		InvocationID: kcp.Identity.InvocationID, Exec: cfg.KcpPath, StartedAt: kcp.PlacedAt, ReadyAt: kcp.ReadyAt}
	for _, other := range kcpserver.SlotNames(kcpserver.ShapeBlueGreen) {
		if other != active {
			delete(slots.Slots, other)
			rec.RemoveUnit(SlotRole(other))
		}
	}
	if err := kcpserver.WriteSlots(l.SlotsFile(), slots); err != nil {
		return err
	}
	fc := apiserverfront.Client{Path: l.FrontControl()}
	backend := "127.0.0.1:" + strconv.Itoa(kcp.Port)
	fst, err := fc.Set(ctx, backend)
	if err != nil {
		return fmt.Errorf("bootstrap: pointing the front at %s: %w", backend, err)
	}
	if kcpPlaced || prevInvocation != kcp.Identity.InvocationID {
		admin, err := kcpserver.NewHTTPAdmin(a, frontURL)
		if err != nil {
			return err
		}
		verdict, err := HandoverStaleLease(ctx, admin, log)
		if err != nil {
			log.Warn("checking the controller lease after a slot start", "err", err.Error())
		}
		log.Info("controller lease after the slot start", "lease", verdict, "seeded", seeded)
	}
	kcp.RSSKiB = RSSKiB(kcp.Identity.PID)
	kine.RSSKiB = RSSKiB(kine.Identity.PID)
	front.RSSKiB = RSSKiB(front.Identity.PID)
	rec.SetUnit(kine)
	rec.SetUnit(front)
	rec.SetUnit(kcp)
	rec.KcpURL = frontURL
	rec.AdminKubeconfig = l.FrontKubeconfig()
	rec.Ready = true
	rec.Updated = time.Now().UTC()
	log.Info("kcp ready behind its front", "slot", active, "port", kcp.Port, "front", fst.Address, "backend", fst.Backend,
		"startToReady", kcp.StartedTo, "kcpRSSKiB", kcp.RSSKiB, "kineRSSKiB", kine.RSSKiB, "frontRSSKiB", front.RSSKiB)
	return nil
}

func Roll(ctx context.Context, m hostsvc.Manager, cfg Config, rec *devinstance.Record) (kcpserver.RollReport, error) {
	l := cfg.Layout
	slots, err := kcpserver.ReadSlots(l.SlotsFile())
	if err != nil {
		return kcpserver.RollReport{}, fmt.Errorf("bootstrap: no kcp-slots.json; dev up first: %w", err)
	}
	a, err := kcpserver.ReadSharedAdmin(l.SharedDir())
	if err != nil {
		return kcpserver.RollReport{}, err
	}
	frontURL := slots.Front.URL()
	admin, err := kcpserver.NewHTTPAdmin(a, frontURL)
	if err != nil {
		return kcpserver.RollReport{}, err
	}
	for _, slot := range kcpserver.SlotNames(kcpserver.ShapeBlueGreen) {
		if err := kcpserver.WriteAuditPolicy(l.SlotRoot(slot)); err != nil {
			return kcpserver.RollReport{}, err
		}
	}
	rep, after, err := kcpserver.Roll(ctx, m, apiserverfront.Client{Path: l.FrontControl()}, admin, kcpserver.RollConfig{
		SlotsFile: l.SlotsFile(), Shared: l.SharedDir(),
		SpecFor: func(slot string) (hostsvc.UnitSpec, string, error) {
			return cfg.slotSpec(slot, slots.KineURL, frontURL)
		},
		ReadyTimeout: cfg.ReadyTimeout, OnListen: cfg.OnListen, Log: cfg.log(),
	})
	if after.Active == rep.To && rep.NewUnit != "" {
		if id, ierr := m.Identify(ctx, rep.NewUnit, 0); ierr == nil {
			rec.SetUnit(devinstance.Unit{Role: SlotRole(rep.To), Unit: rep.NewUnit, Identity: id, Port: rep.NewPort,
				Endpoint: endpoint(SlotRole(rep.To), rep.NewPort), PlacedAt: rep.StartedAt, PortAt: rep.PortAt,
				ReadyAt: rep.ReadyAt, StartedTo: rep.ReadyAt.Sub(rep.StartedAt).Round(time.Millisecond).String(),
				RSSKiB: RSSKiB(id.PID)})
		}
		if !rep.OldExitedAt.IsZero() {
			rec.RemoveUnit(SlotRole(rep.From))
		}
		rec.Updated = time.Now().UTC()
	}
	return rep, err
}
