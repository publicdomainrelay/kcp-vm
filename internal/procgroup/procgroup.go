package procgroup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
)

type Component string

const (
	ComponentPostgres Component = "embedded-postgres"

	ComponentOpenBao Component = "openbao"

	ComponentKine Component = "kine"

	ComponentK3s Component = "k3s-server"

	ComponentCloudControllerManager Component = "socialweb-cloud-controller-manager"

	ComponentApiserverLink Component = "apiserver-link"

	ComponentOpenBaoLink Component = "openbao-link"
)

var DefaultOrder = []Component{
	ComponentPostgres, ComponentKine, ComponentK3s, ComponentCloudControllerManager,
}

var StackOrder = []Component{
	ComponentPostgres, ComponentOpenBao, ComponentKine, ComponentK3s, ComponentCloudControllerManager,
}

type Step struct {
	Name    Component
	Spec    processrunner.Spec
	Prepare func(ctx context.Context, spec processrunner.Spec) (processrunner.Spec, error)
	Ready   func(ctx context.Context, p *processrunner.Process) error
}

type Options struct {
	GracePeriod time.Duration

	Signal os.Signal

	PostgresUp func() bool
}

const DefaultGracePeriod = 10 * time.Second

type Group struct {
	mu     sync.Mutex
	procs  map[Component]*processrunner.Process
	order  []Component
	opts   Options
	closed bool
}

var ErrPostgresUnavailable = errors.New(
	"procgroup: openbao's store is this cluster's own embedded-postgres, and it is not running. " +
		"The vault's data and its unseal material live in that store, so a vault started without " +
		"it would come up on a store that is not its own: refusing to start openbao")

func Spawn(ctx context.Context, steps []Step, opts Options) (*Group, error) {
	if opts.GracePeriod == 0 {
		opts.GracePeriod = DefaultGracePeriod
	}
	if opts.Signal == nil {
		opts.Signal = hostos.GracefulSignal()
	}
	g := &Group{procs: map[Component]*processrunner.Process{}, opts: opts}

	for _, step := range steps {
		g.order = append(g.order, step.Name)

		if step.Name == ComponentOpenBao {
			if opts.PostgresUp == nil || !opts.PostgresUp() {
				_ = g.Reap(ctx)
				return nil, fmt.Errorf("%w: refusing to start openbao", ErrPostgresUnavailable)
			}
		}

		spec := step.Spec
		if step.Prepare != nil {
			prepared, err := step.Prepare(ctx, spec)
			if err != nil {
				_ = g.Reap(ctx)
				return nil, fmt.Errorf("procgroup: preparing %s: %w", step.Name, err)
			}
			spec = prepared
		}

		proc, err := processrunner.Start(context.WithoutCancel(ctx), spec)
		if err != nil {
			_ = g.Reap(ctx)
			return nil, fmt.Errorf("procgroup: start %s: %w", step.Name, err)
		}
		g.procs[step.Name] = proc

		if step.Ready == nil {
			_ = g.Reap(ctx)
			return nil, fmt.Errorf("procgroup: %s has no readiness gate", step.Name)
		}
		if err := step.Ready(ctx, proc); err != nil {
			_ = g.Reap(ctx)
			return nil, fmt.Errorf("procgroup: %s never became ready: %w", step.Name, err)
		}
	}
	return g, nil
}

func (g *Group) Reap(ctx context.Context) error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	procs := make(map[Component]*processrunner.Process, len(g.procs))
	for k, v := range g.procs {
		procs[k] = v
	}
	order := make([]Component, len(g.order))
	copy(order, g.order)
	g.mu.Unlock()

	var errs []error
	for i := len(order) - 1; i >= 0; i-- {
		proc, ok := procs[order[i]]
		if !ok {
			continue
		}
		if err := g.stopWithGrace(ctx, proc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", order[i], err))
		}
	}
	return errors.Join(errs...)
}

func (g *Group) stopWithGrace(ctx context.Context, p *processrunner.Process) error {
	done := make(chan error, 1)
	go func() { done <- p.Stop(g.opts.Signal) }()
	select {
	case err := <-done:
		return err
	case <-time.After(g.opts.GracePeriod):
		return p.Stop(hostos.ForcedSignal())
	case <-ctx.Done():
		return p.Stop(hostos.ForcedSignal())
	}
}

func (g *Group) Process(c Component) (*processrunner.Process, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.procs[c]
	return p, ok
}

func (g *Group) Order() []Component {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Component, len(g.order))
	copy(out, g.order)
	return out
}
