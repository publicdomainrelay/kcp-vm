package materialiser

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

type LocalHost struct {
	mu     sync.Mutex
	groups map[string]*procgroup.Group

	survivors map[string][]string

	opts procgroup.Options

	PostgresUp func(cluster string) bool

	Grace time.Duration
}

func NewLocalHost(opts procgroup.Options) *LocalHost {
	return &LocalHost{
		groups:    map[string]*procgroup.Group{},
		survivors: map[string][]string{},
		opts:      opts,
		Grace:     procgroup.DefaultGracePeriod,
	}
}

func (h *LocalHost) Live(cluster string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.groups[cluster]
	return ok
}

func (h *LocalHost) Spawn(ctx context.Context, cluster string, steps []procgroup.Step) (*procgroup.Group, error) {
	h.mu.Lock()
	if _, ok := h.groups[cluster]; ok {
		h.mu.Unlock()
		return nil, fmt.Errorf("materialiser: a group for %s is already running", cluster)
	}
	h.mu.Unlock()

	opts := h.opts
	if h.PostgresUp != nil {
		opts.PostgresUp = func() bool { return h.PostgresUp(cluster) }
	}

	g, err := procgroup.Spawn(ctx, steps, opts)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.groups[cluster] = g
	h.mu.Unlock()
	return g, nil
}

func (h *LocalHost) Reap(ctx context.Context, cluster string) error {
	h.mu.Lock()
	g, ok := h.groups[cluster]
	delete(h.groups, cluster)
	h.mu.Unlock()
	if !ok {
		return nil
	}
	return g.Reap(ctx)
}

func (h *LocalHost) PIDs(cluster string) []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	g, ok := h.groups[cluster]
	if !ok {
		return nil
	}
	var out []int
	for _, c := range g.Order() {
		if p, ok := g.Process(c); ok {
			out = append(out, p.PID)
		}
	}
	sort.Ints(out)
	return out
}

func (h *LocalHost) Alive(cluster string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.survivors[cluster]...)
}

func (h *LocalHost) Forget(cluster string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.survivors, cluster)
}

func (h *LocalHost) record(handles map[string][]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for cluster, pids := range handles {
		if len(pids) == 0 {
			continue
		}
		held := h.survivors[cluster]
		for _, pid := range pids {
			if slices.Contains(held, pid) {
				continue
			}
			held = append(held, pid)
		}
		h.survivors[cluster] = held
	}
}

func (h *LocalHost) Kill(ctx context.Context, handle string) error {
	pid, err := strconv.Atoi(handle)
	if err != nil {
		return fmt.Errorf("materialiser: handle %q is not a pid", handle)
	}
	if err := hostos.Signal(pid, false); err != nil {
		return err
	}
	grace := h.Grace
	if grace == 0 {
		grace = procgroup.DefaultGracePeriod
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive, err := hostos.Alive(pid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return hostos.Signal(pid, true)
}

type Orphans struct {
	*LocalHost
}

func NewOrphans(h *LocalHost, handles map[string][]string) *Orphans {
	if h == nil {
		panic("materialiser: NewOrphans without a host: the survivors would be recorded on a " +
			"host nothing can kill through, and Alive would then answer with handles no Reap " +
			"and no Kill can ever reach")
	}
	h.record(handles)
	return &Orphans{LocalHost: h}
}
