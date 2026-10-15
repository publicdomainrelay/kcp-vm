package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const (
	ProfileStandIns  = "stand-ins"
	StandInStopGrace = 5 * time.Second
)

var standInRole = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

func StandInUnit(l devinstance.Layout, role string) string {
	return l.Names().StandIn(role)
}

func ReadStandIns(path string) ([]devinstance.StandIn, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s []devinstance.StandIn
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("bootstrap: stand-ins %s: %w", path, err)
	}
	if len(s) == 0 {
		return nil, fmt.Errorf("bootstrap: stand-ins %s names no unit", path)
	}
	seen := map[string]bool{}
	for _, u := range s {
		if !standInRole.MatchString(u.Role) || seen[u.Role] || u.Role == "core" {
			return nil, fmt.Errorf("bootstrap: stand-in role %q: want a unique lower-case name of at most 24, not core", u.Role)
		}
		seen[u.Role] = true
		if len(u.Exec) == 0 || !filepath.IsAbs(u.Exec[0]) {
			return nil, fmt.Errorf("bootstrap: stand-in %s: exec must start with an absolute path (never PATH)", u.Role)
		}
	}
	return s, nil
}

const StandInMemoryMax = 1 << 30

func (c Config) standInSpec(s devinstance.StandIn) hostsvc.UnitSpec {
	l := c.Layout
	log := filepath.Join(l.Logs, s.Role+".log")
	return hostsvc.UnitSpec{Name: StandInUnit(l, s.Role), Description: "stand-in " + s.Role + " of " + l.ID, Exec: s.Exec,
		Env: s.Env, WorkDir: l.StateDir, Slice: l.KcpSlice(), Restart: hostsvc.RestartNo, KillSignal: syscall.SIGTERM,
		StopTimeout: StandInStopGrace, Limits: hostsvc.Limits{MemoryMax: StandInMemoryMax}, Output: hostsvc.Output{StdoutFile: log, StderrFile: log}}
}

func ensureStandIns(ctx context.Context, m hostsvc.Manager, cfg Config, rec *devinstance.Record) error {
	cfg.silent = map[string]bool{}
	for _, s := range rec.StandIns {
		cfg.silent[s.Role] = !s.Listens
	}
	if err := os.MkdirAll(cfg.Layout.Logs, 0o700); err != nil {
		return err
	}
	for _, s := range rec.StandIns {
		u, _, err := ensureUnit(ctx, m, cfg, rec, s.Role, cfg.standInSpec(s), nil)
		if err != nil {
			return fmt.Errorf("bootstrap: stand-in %s: %w", s.Role, err)
		}
		if s.Listens {
			u.Endpoint = "tcp://127.0.0.1:" + strconv.Itoa(u.Port)
			cfg.onListen(s.Role, u.Port)
		}
		rec.SetUnit(u)
	}
	rec.Ready = true
	rec.Updated = time.Now().UTC()
	return nil
}
