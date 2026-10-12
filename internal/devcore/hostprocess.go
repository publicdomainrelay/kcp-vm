package devcore

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap/table"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostprocess"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const HostProcessApplyTimeout = 2 * time.Minute

func Mirrors(bcfg bootstrap.Config, l devinstance.Layout) []hostprocess.Mirror {
	var rec devinstance.Record
	if err := devinstance.ReadJSON(l.Record, &rec); err != nil {
		return nil
	}
	app, err := bcfg.App(&rec)
	if err != nil {
		return nil
	}
	tbl, err := table.Table(app)
	if err != nil {
		return nil
	}
	p := bcfg.Params(app)
	var out []hostprocess.Mirror
	for _, e := range tbl {
		if e.Kind == table.KindSlot && rec.Unit(e.Role) == nil {
			continue
		}
		spec, err := p.Spec(e, rec.KcpURL)
		if err != nil {
			continue
		}
		out = append(out, hostprocess.Mirror{Role: e.Role, Unit: e.Unit, Exec: spec.Exec, Slice: spec.Slice, After: e.After, Wants: e.Wants})
	}
	return out
}

func HostProcessConfig(bcfg bootstrap.Config, rec devinstance.Record, m hostsvc.Manager, log *slog.Logger) hostprocess.Config {
	l := bcfg.Layout
	ws := rec.BaseWorkspace
	if ws == "" {
		ws = hostprocess.DefaultWorkspace
	}
	return hostprocess.Config{Kubeconfig: rec.AdminKubeconfig, Workspace: ws, Manager: m, Prefix: l.Names().HostProcessPrefix(),
		Resolve: func(a v1alpha1.HostProcessAsset) (string, error) {
			if e, ok := rec.Executables[a.Name]; ok && e.Path != "" {
				return e.Path, nil
			}
			return "", fmt.Errorf("devcore: no executable %s in this dev instance", a.Name)
		},
		Mirrors:      func() []hostprocess.Mirror { return Mirrors(bcfg, l) },
		ApplyTimeout: HostProcessApplyTimeout, Log: log}
}
