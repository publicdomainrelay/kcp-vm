package kcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/apiserverfront"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const (
	DefaultRollReadyTimeout = 5 * time.Minute
	DefaultLeaseTimeout     = 30 * time.Second
	DefaultProbeTimeout     = 10 * time.Second
	DefaultDrainTimeout     = 5 * time.Second
	DefaultRollPoll         = 200 * time.Millisecond
	PortPoll                = 20 * time.Millisecond
)

type FrontSwitch interface {
	Set(ctx context.Context, backend string) (apiserverfront.Status, error)
	Status(ctx context.Context) (apiserverfront.Status, error)
	Drain(ctx context.Context, backend string) (int, error)
}

type ShardAdmin interface {
	Readyz(ctx context.Context, port int) (int, error)
	Lease(ctx context.Context) (Lease, error)
	DeleteLease(ctx context.Context) error
	ProbeWorkspace(ctx context.Context, name string) error
}

type RollConfig struct {
	SlotsFile    string
	Shared       string
	SpecFor      func(slot string) (hostsvc.UnitSpec, string, error)
	KineFor      func(slot string) string
	Backup       func(ctx context.Context) (string, error)
	ListenPort   func(ctx context.Context, pid int) (int, error)
	OnListen     func(name string, addr net.Addr)
	Poll         time.Duration
	ReadyTimeout time.Duration
	LeaseTimeout time.Duration
	ProbeTimeout time.Duration
	DrainTimeout time.Duration
	Log          *slog.Logger
}

type RollReport struct {
	From                 string    `json:"from"`
	To                   string    `json:"to"`
	OldUnit              string    `json:"oldUnit"`
	StoreBackup          string    `json:"storeBackup,omitempty"`
	BackupSeconds        float64   `json:"backupSeconds,omitempty"`
	NewUnit              string    `json:"newUnit"`
	OldPort              int       `json:"oldPort"`
	NewPort              int       `json:"newPort"`
	NewPID               int       `json:"newPID"`
	NewInvocationID      string    `json:"newInvocationID"`
	OldHolder            string    `json:"oldHolder"`
	NewHolder            string    `json:"newHolder"`
	StartedAt            time.Time `json:"startedAt"`
	PortAt               time.Time `json:"portAt"`
	ReadyAt              time.Time `json:"readyAt"`
	PersistedAt          time.Time `json:"persistedAt"`
	FlippedAt            time.Time `json:"flippedAt"`
	SigtermAt            time.Time `json:"sigtermAt"`
	OldExitedAt          time.Time `json:"oldExitedAt"`
	LeaseDeletedAt       time.Time `json:"leaseDeletedAt"`
	LeaseHeldAt          time.Time `json:"leaseHeldAt"`
	ProbeReadyAt         time.Time `json:"probeReadyAt"`
	DoneAt               time.Time `json:"doneAt"`
	StartToReadySeconds  float64   `json:"startToReadySeconds"`
	OldExitSeconds       float64   `json:"oldExitSeconds"`
	LeaseHandoverSeconds float64   `json:"leaseHandoverSeconds"`
	ProbeSeconds         float64   `json:"probeSeconds"`
	TotalSeconds         float64   `json:"totalSeconds"`
	OpenOnOldAtExit      int       `json:"openOnOldAtExit"`
	Drained              int       `json:"drained"`
}

func (c *RollConfig) defaults() {
	if c.Poll <= 0 {
		c.Poll = DefaultRollPoll
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = DefaultRollReadyTimeout
	}
	if c.LeaseTimeout <= 0 {
		c.LeaseTimeout = DefaultLeaseTimeout
	}
	if c.ProbeTimeout <= 0 {
		c.ProbeTimeout = DefaultProbeTimeout
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = DefaultDrainTimeout
	}
	if c.ListenPort == nil {
		c.ListenPort = func(ctx context.Context, pid int) (int, error) { return hostsvc.WaitListenPort(ctx, pid, PortPoll) }
	}
	if c.Log == nil {
		c.Log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
}

func backendOf(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }

func WaitOwnPortReady(ctx context.Context, admin ShardAdmin, port int, every time.Duration) error {
	var last error
	for {
		code, err := admin.Readyz(ctx, port)
		if err == nil && code == http.StatusOK {
			return nil
		}
		last = fmt.Errorf("readyz on 127.0.0.1:%d: %d %v", port, code, err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("kcpserver: not ready: %v (%w)", last, ctx.Err())
		case <-time.After(every):
		}
	}
}

func WaitNewHolder(ctx context.Context, admin ShardAdmin, old string, every time.Duration) (Lease, error) {
	var last error
	for {
		l, err := admin.Lease(ctx)
		if err == nil && l.Found && l.Holder != "" && l.Holder != old {
			return l, nil
		}
		last = err
		if err == nil {
			last = fmt.Errorf("holder %q", l.Holder)
		}
		select {
		case <-ctx.Done():
			return Lease{}, fmt.Errorf("kcpserver: no new holder of %s/%s: %v (%w)", ControllerLeaseNamespace, ControllerLeaseName, last, ctx.Err())
		case <-time.After(every):
		}
	}
}

func secs(a, b time.Time) float64 { return float64(b.Sub(a).Milliseconds()) / 1000 }

func Roll(ctx context.Context, m hostsvc.Manager, front FrontSwitch, admin ShardAdmin, cfg RollConfig) (RollReport, Slots, error) {
	cfg.defaults()
	log := cfg.Log.With("component", "kcp-roll")
	s, err := ReadSlots(cfg.SlotsFile)
	if err != nil {
		return RollReport{}, Slots{}, fmt.Errorf("kcpserver: reading %s: %w", cfg.SlotsFile, err)
	}
	if s.Shape != ShapeBlueGreen {
		return RollReport{}, s, ErrSingleSlot
	}
	from := s.Active
	old, ok := s.Slots[from]
	if !ok || old.Unit == "" {
		return RollReport{}, s, fmt.Errorf("kcpserver: %s names active slot %q, which it does not record", cfg.SlotsFile, from)
	}
	to := Other(from)
	rep := RollReport{From: from, To: to, OldUnit: old.Unit, OldPort: old.Port, StartedAt: time.Now().UTC()}
	if l, err := admin.Lease(ctx); err == nil {
		rep.OldHolder = l.Holder
	}
	if cfg.Backup != nil {
		path, err := cfg.Backup(ctx)
		if err != nil {
			return rep, s, fmt.Errorf("kcpserver: the store backup before the roll: %w", err)
		}
		rep.StoreBackup, rep.BackupSeconds = path, secs(rep.StartedAt, time.Now().UTC())
		log.Info("store backed up before the roll", "path", path, "seconds", rep.BackupSeconds)
	}
	spec, root, err := cfg.SpecFor(to)
	if err != nil {
		return rep, s, err
	}
	rep.NewUnit = spec.Name
	if err := SeedSlot(cfg.Shared, root); err != nil {
		return rep, s, err
	}
	m.Stop(ctx, spec.Name)
	m.ResetFailed(ctx, spec.Name)
	fail := func(err error) (RollReport, Slots, error) {
		log.Error("roll aborted before the flip; stopping the new slot", "slot", to, "err", err.Error())
		sctx, cancel := context.WithTimeout(context.Background(), SlotStopTimeout+5*time.Second)
		defer cancel()
		m.Stop(sctx, spec.Name)
		m.ResetFailed(sctx, spec.Name)
		return rep, s, err
	}
	st, err := m.Start(ctx, spec)
	if err != nil {
		return fail(err)
	}
	if st.Adopted {
		return fail(fmt.Errorf("kcpserver: %s was already running after its stop; refusing to adopt a stale slot", spec.Name))
	}
	rep.NewPID, rep.NewInvocationID = st.MainPID, st.InvocationID
	pctx, cancel := context.WithTimeout(ctx, cfg.ReadyTimeout)
	defer cancel()
	port, err := cfg.ListenPort(pctx, st.MainPID)
	if err != nil {
		return fail(fmt.Errorf("kcpserver: reading the bound port of %s (pid %d): %w", spec.Name, st.MainPID, err))
	}
	rep.NewPort, rep.PortAt = port, time.Now().UTC()
	if cfg.OnListen != nil {
		cfg.OnListen("kcp-"+to, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	}
	log.Info("new slot listening", "slot", to, "port", port, "pid", st.MainPID)
	if err := WaitOwnPortReady(pctx, admin, port, cfg.Poll); err != nil {
		return fail(err)
	}
	rep.ReadyAt = time.Now().UTC()
	rep.StartToReadySeconds = secs(rep.StartedAt, rep.ReadyAt)
	prev := s
	prev.Slots = map[string]SlotRecord{}
	for k, v := range s.Slots {
		prev.Slots[k] = v
	}
	s.Slots[to] = SlotRecord{Unit: spec.Name, Root: root, Port: port, PID: st.MainPID, InvocationID: st.InvocationID,
		Exec: spec.Exec[0], StartedAt: rep.StartedAt, ReadyAt: rep.ReadyAt}
	if cfg.KineFor != nil {
		rec := s.Slots[to]
		rec.Kine = cfg.KineFor(to)
		s.Slots[to] = rec
		s.KineURL = rec.Kine
	}
	s.Active = to
	if err := WriteSlots(cfg.SlotsFile, s); err != nil {
		s = prev
		return fail(err)
	}
	rep.PersistedAt = time.Now().UTC()
	if _, err := front.Set(ctx, backendOf(port)); err != nil {
		WriteSlots(cfg.SlotsFile, prev)
		s = prev
		return fail(fmt.Errorf("kcpserver: flipping the front: %w", err))
	}
	rep.FlippedAt = time.Now().UTC()
	log.Info("front flipped", "from", backendOf(old.Port), "to", backendOf(port))
	rep.SigtermAt = time.Now().UTC()
	if err := m.Stop(ctx, old.Unit); err != nil {
		return rep, s, fmt.Errorf("kcpserver: stopping the old slot %s: %w", old.Unit, err)
	}
	if ost, err := m.Inspect(ctx, old.Unit); err == nil && (ost.State == hostsvc.Active || ost.State == hostsvc.Deactivating) {
		return rep, s, fmt.Errorf("kcpserver: the old slot %s is still %s after its stop", old.Unit, ost.State)
	}
	m.ResetFailed(ctx, old.Unit)
	rep.OldExitedAt = time.Now().UTC()
	rep.OldExitSeconds = secs(rep.SigtermAt, rep.OldExitedAt)
	if fst, err := front.Status(ctx); err == nil {
		rep.OpenOnOldAtExit = fst.Open[backendOf(old.Port)]
	}
	if err := admin.DeleteLease(ctx); err != nil {
		return rep, s, err
	}
	rep.LeaseDeletedAt = time.Now().UTC()
	lctx, lcancel := context.WithTimeout(ctx, cfg.LeaseTimeout)
	defer lcancel()
	l, err := WaitNewHolder(lctx, admin, rep.OldHolder, cfg.Poll)
	if err != nil {
		return rep, s, err
	}
	rep.NewHolder, rep.LeaseHeldAt = l.Holder, time.Now().UTC()
	rep.LeaseHandoverSeconds = secs(rep.OldExitedAt, rep.LeaseHeldAt)
	log.Info("controller lease handed over", "from", rep.OldHolder, "to", rep.NewHolder, "seconds", rep.LeaseHandoverSeconds)
	probe := "roll-probe-" + strconv.FormatInt(time.Now().UnixMilli(), 36)
	prctx, prcancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
	defer prcancel()
	if err := admin.ProbeWorkspace(prctx, probe); err != nil {
		return rep, s, fmt.Errorf("kcpserver: the probe workspace did not reconcile after the roll: %w", err)
	}
	rep.ProbeReadyAt = time.Now().UTC()
	rep.ProbeSeconds = secs(rep.LeaseHeldAt, rep.ProbeReadyAt)
	deadline := time.Now().Add(cfg.DrainTimeout)
	for {
		fst, err := front.Status(ctx)
		if err != nil || fst.Open[backendOf(old.Port)] == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(cfg.Poll)
	}
	if n, err := front.Drain(ctx, backendOf(old.Port)); err == nil {
		rep.Drained = n
	} else if !errors.Is(err, apiserverfront.ErrCurrentBackend) {
		log.Warn("draining the old backend", "err", err.Error())
	}
	delete(s.Slots, from)
	if err := WriteSlots(cfg.SlotsFile, s); err != nil {
		return rep, s, err
	}
	rep.DoneAt = time.Now().UTC()
	rep.TotalSeconds = secs(rep.StartedAt, rep.DoneAt)
	return rep, s, nil
}
