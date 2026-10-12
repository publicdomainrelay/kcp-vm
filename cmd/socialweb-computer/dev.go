package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devcore"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devsup"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/pgunit"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/stack/denoruntime"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

const (
	devsupReadyTimeout = 15 * time.Second
	devsupExitTimeout  = 3 * time.Minute
	coreStopTimeout    = 10 * time.Second
)

func (a *app) fail(code int, format string, args ...any) int {
	fmt.Fprintf(a.stderr, format+"\n", args...)
	return code
}

func (a *app) printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(a.stdout, string(b))
}

func (a *app) layout(vals clicfg.Values) (devinstance.Layout, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return devinstance.Layout{}, err
	}
	id, err := devinstance.ID(vals.String("instance"), cwd, a.env)
	if err != nil {
		return devinstance.Layout{}, err
	}
	root, err := devinstance.StateRoot(vals.String("state-dir"), a.env)
	if err != nil {
		return devinstance.Layout{}, err
	}
	rt, err := devinstance.RuntimeRoot(a.env)
	if err != nil {
		return devinstance.Layout{}, err
	}
	return devinstance.NewLayout(id, root, rt)
}

func (a *app) worktree(vals clicfg.Values) string {
	if vals.String("instance") != "" {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return ""
	}
	return devinstance.WorktreeRoot(real)
}

func (a *app) leases(id string) (*portallocator.Allocator, error) {
	path, err := devinstance.LeasePath(a.env)
	if err != nil {
		return nil, err
	}
	return devinstance.OpenLeases(path, id)
}

func (a *app) instanceLeases(id string) []portallocator.Lease {
	al, err := a.leases(id)
	if err != nil {
		a.log.Warn("the per-user lease file", "err", err.Error())
		return nil
	}
	return al.Leases()
}

func writeEnvFiles(l devinstance.Layout, ex []devinstance.Export) error {
	if err := os.WriteFile(filepath.Join(l.StateDir, devinstance.EnvFile), []byte(devinstance.ShellExports(ex)), 0o600); err != nil {
		return err
	}
	return devinstance.WriteJSON(filepath.Join(l.StateDir, devinstance.EnvJSONFile), exportMap(ex))
}

func exportMap(ex []devinstance.Export) map[string]string {
	out := map[string]string{}
	for _, e := range ex {
		out[e.Name] = e.Value
	}
	return out
}

func coreSpec(rec *devinstance.Record, exe string, readyTimeout time.Duration) (hostsvc.UnitSpec, error) {
	return bootstrap.Config{Layout: rec.Layout, Exe: exe, ReadyTimeout: readyTimeout}.CoreSpec(rec)
}

func (a *app) postgresRoot(vals clicfg.Values, profile string) (devinstance.Executable, error) {
	root := vals.String("postgres-root")
	if assets.Release() {
		if root != "" {
			return devinstance.Executable{}, errors.New("postgres-root is a dev-build option; this release build runs its embedded postgres")
		}
		cache, err := a.assetCache(vals)
		if err != nil {
			return devinstance.Executable{}, err
		}
		e, err := assets.Resolve(assets.Options{Cache: cache, Log: a.log}, pgunit.Asset)
		if err != nil {
			return devinstance.Executable{}, err
		}
		if root, err = pgunit.RootOf(e.Path); err != nil {
			return devinstance.Executable{}, err
		}
	}
	if root == "" {
		if bootstrap.NeedsPostgres(profile) {
			return devinstance.Executable{}, fmt.Errorf("--profile %s runs kine on postgres: postgres-root must name the absolute root of the extracted postgres asset tree (versions.json postgres; never PATH)", profile)
		}
		return devinstance.Executable{}, nil
	}
	in, err := pgunit.FromRoot(root)
	if err != nil {
		return devinstance.Executable{}, err
	}
	e, err := hashExe("postgres-root", in.Path("postgres"))
	if err != nil {
		return devinstance.Executable{}, err
	}
	return devinstance.Executable{Path: root, SHA256: e.SHA256, Version: in.Version}, nil
}

func selfExe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func checkExe(ctx context.Context, option, path string) (devinstance.Executable, error) {
	e, err := hashExe(option, path)
	if err != nil {
		return e, err
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(vctx, path, "--version").CombinedOutput()
	e.Version = strings.TrimSpace(string(out))
	return e, nil
}

func hashExe(option, path string) (devinstance.Executable, error) {
	if path == "" {
		return devinstance.Executable{}, fmt.Errorf("%s is required in this dev build (an absolute path; never PATH)", option)
	}
	if !filepath.IsAbs(path) {
		return devinstance.Executable{}, fmt.Errorf("%s %q must be an absolute path; this build never looks in PATH", option, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return devinstance.Executable{}, fmt.Errorf("%s: %w", option, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return devinstance.Executable{}, fmt.Errorf("%s %s is not an executable file", option, path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return devinstance.Executable{}, err
	}
	return devinstance.Executable{Path: path, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func lockFile(path string, wait time.Duration) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := hostos.Lock(f, hostos.LockExclusive|hostos.LockNonBlocking)
		if err == nil {
			return f, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s is held by another dev up or dev down of this instance", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func procConfig(l devinstance.Layout) hostsvc.Config {
	return hostsvc.Config{Backend: hostsvc.BackendProc, ProcSocket: l.Socket}
}

func helloDevsup(ctx context.Context, l devinstance.Layout) (hostsvc.ProcClient, hostsvc.ProcHello, error) {
	c, err := hostsvc.OpenProcClient(ctx, procConfig(l))
	if err != nil {
		return nil, hostsvc.ProcHello{}, err
	}
	h, err := c.Hello(ctx)
	if err != nil {
		return nil, hostsvc.ProcHello{}, err
	}
	return c, h, nil
}

var errDevsupDetached = errors.New("the instance is already up with a detached devsup")

func startOrAdoptDevsup(ctx context.Context, a *app, l devinstance.Layout, exe string, dieWith int, fg bool) (hostsvc.ProcClient, devinstance.Devsup, error) {
	if c, h, err := helloDevsup(ctx, l); err == nil {
		if fg {
			c.Close()
			return nil, devinstance.Devsup{}, fmt.Errorf("%w (devsup pid %d); dev down it first, or run dev up without --fg", errDevsupDetached, h.PID)
		}
		return c, devinstance.Devsup{Instance: h.Instance, PID: h.PID, StartTicks: h.StartTicks, BootID: h.BootID, Adopted: true}, nil
	}
	if err := os.MkdirAll(l.SupState, 0o700); err != nil {
		return nil, devinstance.Devsup{}, err
	}
	argv := []string{exe, "internal", "devsup", "--state", l.SupState, "--socket", l.Socket, "--instance", l.ID}
	if dieWith != 0 {
		argv = append(argv, "--die-with", strconv.Itoa(dieWith))
	}
	pid, err := devinstance.SpawnDetached(argv, filepath.Join(l.SupState, "devsup.log"))
	if err != nil {
		return nil, devinstance.Devsup{}, fmt.Errorf("starting devsup: %w", err)
	}
	deadline := time.Now().Add(devsupReadyTimeout)
	for {
		c, h, err := helloDevsup(ctx, l)
		if err == nil && h.PID == pid {
			a.log.Info("devsup started", "pid", pid, "instance", h.Instance, "socket", l.Socket)
			return c, devinstance.Devsup{Instance: h.Instance, PID: h.PID, StartTicks: h.StartTicks, BootID: h.BootID}, nil
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(l.SupState, "devsup.log"))
			return nil, devinstance.Devsup{}, fmt.Errorf("devsup pid %d did not answer on %s: %v\n%s", pid, l.Socket, err, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type upReport struct {
	Instance        string                            `json:"instance"`
	Profile         string                            `json:"profile"`
	StateDir        string                            `json:"stateDir"`
	Socket          string                            `json:"socket"`
	AdminKubeconfig string                            `json:"adminKubeconfig"`
	KcpURL          string                            `json:"kcpURL"`
	KineURL         string                            `json:"kineURL"`
	Devsup          devinstance.Devsup                `json:"devsup"`
	Units           []devinstance.Unit                `json:"units"`
	Core            devinstance.Core                  `json:"core"`
	StartedAt       time.Time                         `json:"startedAt"`
	ReadyAt         time.Time                         `json:"readyAt"`
	Seconds         float64                           `json:"seconds"`
	Env             map[string]string                 `json:"env"`
	EnvFile         string                            `json:"envFile"`
	Marker          string                            `json:"marker,omitempty"`
	Foreground      bool                              `json:"foreground,omitempty"`
	Executables     map[string]devinstance.Executable `json:"executables"`
}

func runDevUp(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	started := time.Now().UTC()
	if err := devinstance.RefuseRoot(os.Geteuid()); err != nil {
		return a.fail(exitRefused, "dev up: %v", err)
	}
	if b := vals.String(backendOpt); b != "" && b != string(hostsvc.BackendProc) {
		return a.fail(exitRefused, "dev up: host-backend=%s: dev places every unit under devsup (proc); systemd-user is for install", b)
	}
	profile, standInsFile := vals.String("profile"), vals.String("stand-ins")
	var standIns []devinstance.StandIn
	switch {
	case profile != "" && standInsFile != "":
		return a.fail(exitRefused, "dev up: --profile %s and --stand-ins name two bootstrap layers; pass one", profile)
	case profile == "" && standInsFile == "":
		return a.fail(exitRefused, "dev up: name a profile (--profile; built: quick, prodlike) or the units to place (--stand-ins)")
	case standInsFile != "":
		s, err := bootstrap.ReadStandIns(standInsFile)
		if err != nil {
			return a.fail(exitRefused, "dev up: %v", err)
		}
		standIns, profile = s, bootstrap.ProfileStandIns
	default:
		if err := bootstrap.CheckProfile(profile); err != nil {
			return a.fail(exitRefused, "dev up: %v", err)
		}
	}
	readyTimeout, err := time.ParseDuration(vals.String("ready-timeout"))
	if err != nil {
		return a.fail(exitRefused, "dev up: ready-timeout: %v", err)
	}
	var kcpExe, kineExe devinstance.Executable
	pgExe, err := a.postgresRoot(vals, profile)
	if err != nil {
		return a.fail(exitRefused, "dev up: %v", err)
	}
	if standIns == nil {
		if kcpExe, err = a.devExe(ctx, vals, "kcp", "kcp-path"); err != nil {
			return a.fail(exitRefused, "dev up: %v", err)
		}
		if want, err := versions.Get("kcp"); err == nil && want.Version != "" && !strings.Contains(kcpExe.Version, want.Version) {
			return a.fail(exitRefused, "dev up: kcp-path %s reports %q; versions.json pins %s", kcpExe.Path, kcpExe.Version, want.Version)
		}
		if kineExe, err = a.devExe(ctx, vals, "kine", "kine-path"); err != nil {
			return a.fail(exitRefused, "dev up: %v", err)
		}
	}
	denoExe, err := a.denoExe(ctx, vals)
	if err != nil {
		return a.fail(exitRefused, "dev up: %v", err)
	}
	sourceRoot, err := closureSourceRoot(vals)
	if err != nil {
		return a.fail(exitRefused, "dev up: %v", err)
	}
	exe, err := selfExe()
	if err != nil {
		return a.fail(exitFailed, "dev up: %v", err)
	}
	l, err := a.layout(vals)
	if err != nil {
		return a.fail(exitRefused, "dev up: %v", err)
	}
	for _, d := range []string{l.StateDir, l.RuntimeDir, l.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return a.fail(exitFailed, "dev up: %v", err)
		}
		os.Chmod(d, 0o700)
	}
	lock, err := lockFile(l.Lock, 30*time.Second)
	if err != nil {
		return a.fail(exitFailed, "dev up: %v", err)
	}
	fg := vals.Bool("fg")
	dieWith := vals.Int("die-with")
	if fg {
		dieWith = os.Getpid()
	}
	m, sup, err := startOrAdoptDevsup(ctx, a, l, exe, dieWith, fg)
	if errors.Is(err, errDevsupDetached) {
		lock.Close()
		return a.fail(exitRefused, "dev up: %v", err)
	}
	if err != nil {
		lock.Close()
		return a.fail(exitFailed, "dev up: %v", err)
	}
	defer m.Close()
	var rec devinstance.Record
	if err := devinstance.ReadJSON(l.Record, &rec); err != nil {
		rec = devinstance.Record{Created: started}
	}
	rec.Layout, rec.Profile, rec.Backend, rec.Devsup, rec.StandIns = l, profile, hostsvc.BackendProc, sup, standIns
	rec.Worktree = a.worktree(vals)
	rec.BaseWorkspace = vals.String("kcp-base-workspace")
	rec.Executables = map[string]devinstance.Executable{"kcp": kcpExe, "kine": kineExe, "postgres": pgExe, denorun.Executable: denoExe, "socialweb-computer": {Path: exe}}
	rec.SourceRoot = sourceRoot
	bcfg := bootstrap.Config{Layout: l, KcpPath: kcpExe.Path, KinePath: kineExe.Path, PostgresRoot: pgExe.Path, Exe: exe,
		ReadyTimeout: readyTimeout, Log: a.log}
	if err := bootstrap.Ensure(ctx, m, bcfg, &rec); err != nil {
		devinstance.WriteJSON(l.Record, rec)
		lock.Close()
		return a.fail(exitFailed, "dev up: %v", err)
	}
	if err := devinstance.WriteJSON(l.Record, rec); err != nil {
		lock.Close()
		return a.fail(exitFailed, "dev up: %v", err)
	}
	lock.Close()
	cspec, err := coreSpec(&rec, exe, readyTimeout)
	if err != nil {
		return a.fail(exitFailed, "dev up: %v", err)
	}
	cst, err := m.Start(ctx, cspec)
	if err != nil {
		return a.fail(exitFailed, "dev up: placing the core: %v", err)
	}
	core, err := waitCore(ctx, l, cst.InvocationID, readyTimeout)
	if err != nil {
		return a.fail(exitFailed, "dev up: %v", err)
	}
	devinstance.ReadJSON(l.Record, &rec)
	ex := devinstance.Exports(rec, a.instanceLeases(l.ID))
	if err := writeEnvFiles(l, ex); err != nil {
		return a.fail(exitFailed, "dev up: %v", err)
	}
	marker := ""
	if rec.Worktree != "" {
		if err := devinstance.LinkMarker(rec.Worktree, l.StateDir); err != nil {
			a.log.Warn("worktree marker", "err", err.Error())
		} else {
			marker = filepath.Join(rec.Worktree, devinstance.Marker)
		}
	}
	ready := time.Now().UTC()
	a.printJSON(upReport{Instance: l.ID, Profile: rec.Profile, StateDir: l.StateDir, Socket: l.Socket,
		AdminKubeconfig: rec.AdminKubeconfig, KcpURL: rec.KcpURL, KineURL: rec.KineURL, Devsup: sup, Units: rec.Units,
		Core: core, StartedAt: started, ReadyAt: ready, Seconds: ready.Sub(started).Seconds(),
		Env: exportMap(ex), EnvFile: filepath.Join(l.StateDir, devinstance.EnvFile), Marker: marker, Foreground: fg,
		Executables: rec.Executables})
	if !fg {
		return exitOK
	}
	a.log.Info("dev up --fg: the instance lives as long as this process; Ctrl-C is dev down", "instance", l.ID)
	<-ctx.Done()
	r := downInstance(context.Background(), a, l, vals.Bool("keep-state"))
	a.printJSON(r)
	if len(r.Remaining) > 0 {
		return exitFailed
	}
	return exitOK
}

func waitCore(ctx context.Context, l devinstance.Layout, invocation string, timeout time.Duration) (devinstance.Core, error) {
	deadline := time.Now().Add(timeout)
	for {
		var c devinstance.Core
		if err := devinstance.ReadJSON(l.CoreRecord, &c); err == nil && c.InvocationID == invocation && !c.AdoptedAt.IsZero() {
			return c, nil
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(l.Logs, "core.log"))
			return devinstance.Core{}, fmt.Errorf("the core (invocation %s) did not record its adoption within %s\n%s", invocation, timeout, b)
		}
		select {
		case <-ctx.Done():
			return devinstance.Core{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type statusReport struct {
	Instance string              `json:"instance"`
	Found    bool                `json:"found"`
	Record   *devinstance.Record `json:"record,omitempty"`
	Core     *devinstance.Core   `json:"core,omitempty"`
	Devsup   struct {
		Reachable bool                 `json:"reachable"`
		Hello     *hostsvc.ProcHello   `json:"hello,omitempty"`
		Units     []hostsvc.UnitStatus `json:"units,omitempty"`
		Error     string               `json:"error,omitempty"`
	} `json:"devsup"`
	Ports  map[string]int        `json:"ports,omitempty"`
	Leases []portallocator.Lease `json:"leases"`
}

func runDevStatus(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	l, err := a.layout(vals)
	if err != nil {
		return a.fail(exitRefused, "dev status: %v", err)
	}
	r := statusReport{Instance: l.ID, Leases: a.instanceLeases(l.ID)}
	var rec devinstance.Record
	if err := devinstance.ReadJSON(l.Record, &rec); err == nil {
		r.Found, r.Record = true, &rec
		r.Ports = map[string]int{}
		for _, u := range rec.Units {
			r.Ports[u.Role] = u.Port
		}
	}
	var core devinstance.Core
	if err := devinstance.ReadJSON(l.CoreRecord, &core); err == nil {
		r.Core = &core
	}
	if c, h, err := helloDevsup(ctx, l); err == nil {
		r.Devsup.Reachable, r.Devsup.Hello = true, &h
		for _, prefix := range []string{l.Names().UnitPrefix(), l.Names().CorePrefix()} {
			units, err := c.List(ctx, prefix)
			if err != nil {
				r.Devsup.Error = err.Error()
			}
			r.Devsup.Units = append(r.Devsup.Units, units...)
		}
	} else {
		r.Devsup.Error = err.Error()
	}
	a.printJSON(r)
	if !r.Found {
		return exitFailed
	}
	return exitOK
}

type downReport struct {
	Instance       string               `json:"instance"`
	LeasesReleased int                  `json:"leasesReleased"`
	MarkerRemoved  bool                 `json:"markerRemoved"`
	DevsupStopped  bool                 `json:"devsupStopped"`
	DevsupPID      int                  `json:"devsupPID,omitempty"`
	Swept          []devsup.SweepReport `json:"swept,omitempty"`
	LeftoverKilled []int                `json:"leftoverKilled"`
	Remaining      []int                `json:"remaining"`
	RemovedState   bool                 `json:"removedState"`
	RemovedRuntime bool                 `json:"removedRuntime"`
	Seconds        float64              `json:"seconds"`
}

func runDevDown(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	l, err := a.layout(vals)
	if err != nil {
		return a.fail(exitRefused, "dev down: %v", err)
	}
	if _, err := os.Stat(l.StateDir); err == nil {
		lock, err := lockFile(l.Lock, 2*time.Minute)
		if err != nil {
			return a.fail(exitFailed, "dev down: %v", err)
		}
		defer lock.Close()
	}
	r := downInstance(ctx, a, l, vals.Bool("keep-state"))
	a.printJSON(r)
	if len(r.Remaining) > 0 {
		return exitFailed
	}
	return exitOK
}

func downInstance(ctx context.Context, a *app, l devinstance.Layout, keepState bool) downReport {
	started := time.Now()
	r := downReport{Instance: l.ID, LeftoverKilled: []int{}, Remaining: []int{}}
	var rec devinstance.Record
	devinstance.ReadJSON(l.Record, &rec)
	if c, h, err := helloDevsup(ctx, l); err == nil {
		r.DevsupPID = h.PID
		if err := c.Shutdown(ctx); err != nil {
			a.log.Warn("devsup shutdown", "err", err.Error())
		}
		deadline := time.Now().Add(devsupExitTimeout)
		for devsup.Alive(h.PID) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		r.DevsupStopped = !devsup.Alive(h.PID)
	} else {
		a.log.Info("devsup unreachable; sweeping its records", "err", err.Error())
		if rep, err := devsup.SweepOffline(l.SupState); err == nil {
			r.Swept = rep
		}
	}
	if pids := devsup.KillInstance(l.ID); len(pids) > 0 {
		r.LeftoverKilled = pids
	}
	time.Sleep(100 * time.Millisecond)
	if left := devsup.InstanceProcesses(l.ID); len(left) > 0 {
		r.Remaining = left
	}
	if err := os.RemoveAll(l.RuntimeDir); err == nil {
		r.RemovedRuntime = true
		os.Remove(filepath.Dir(l.RuntimeDir))
	}
	if !keepState {
		if rec.Worktree != "" {
			r.MarkerRemoved = devinstance.UnlinkMarker(rec.Worktree, l.StateDir)
		}
		if al, err := a.leases(l.ID); err == nil {
			r.LeasesReleased = len(al.Leases())
			if err := al.ReleaseAll(); err != nil {
				a.log.Warn("releasing the instance's leases", "err", err.Error())
			}
		}
		if err := os.RemoveAll(l.StateDir); err == nil {
			r.RemovedState = true
		}
	}
	r.Seconds = time.Since(started).Seconds()
	return r
}

func runDevsup(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	if err := devinstance.RefuseRoot(os.Geteuid()); err != nil {
		return a.fail(exitRefused, "internal devsup: %v", err)
	}
	for _, n := range []string{"state", "socket"} {
		if err := a.spec.Require(vals, n); err != nil {
			return a.fail(exitRefused, "internal devsup: %v", err)
		}
	}
	err := devsup.Run(ctx, devsup.Options{State: vals.String("state"), Socket: vals.String("socket"),
		DieWith: vals.Int("die-with"), Instance: vals.String("instance"), Log: a.log})
	if err != nil {
		return a.fail(exitFailed, "internal devsup: %v", err)
	}
	return exitOK
}

func runDevCore(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	if err := a.spec.Require(vals, "instance-dir"); err != nil {
		return a.fail(exitRefused, "internal dev-core: %v", err)
	}
	var rec devinstance.Record
	if err := devinstance.ReadJSON(filepath.Join(vals.String("instance-dir"), "instance.json"), &rec); err != nil {
		return a.fail(exitFailed, "internal dev-core: %v", err)
	}
	timeout, err := time.ParseDuration(vals.String("ready-timeout"))
	if err != nil {
		return a.fail(exitRefused, "internal dev-core: ready-timeout: %v", err)
	}
	os.Setenv(denoruntime.WatchListEnv, "false")
	deno, err := a.coreDenoRuntime(vals, rec, timeout)
	if err != nil {
		return a.fail(exitFailed, "internal dev-core: %v", err)
	}
	m, err := hostsvc.Open(ctx, procConfig(rec.Layout))
	if err != nil {
		return a.fail(exitFailed, "internal dev-core: %v", err)
	}
	defer m.Close()
	if err := devcore.Run(ctx, devcore.Config{Layout: rec.Layout, ReadyTimeout: timeout, Log: a.log, DenoRuntime: &deno}, m); err != nil {
		if errors.Is(err, context.Canceled) {
			return exitOK
		}
		return a.fail(exitFailed, "internal dev-core: %v", err)
	}
	return exitOK
}
