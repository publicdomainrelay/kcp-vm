package provider

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	RunDirKeep    = time.Hour
	RunLogCap     = 16 << 20
	RunsKeepEvery = time.Minute
)

type runRemover interface {
	Remove(ctx context.Context, runID string) error
}

type runsKeeper interface {
	Sweep(named func(string) bool, before time.Time) []string
	CapLogs(capBytes int64) []string
	Reap(named func(string) bool, startedBefore time.Time) []string
}

func (p *Provider) removeRunDirs(ctx context.Context, r any, kind, name string, ids ...string) {
	rm, ok := r.(runRemover)
	if !ok {
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if err := rm.Remove(ctx, id); err != nil {
			p.opts.Log.Warn("the runs directory of a deleted object was not removed; the runs sweep retries", "kind", kind, "name", name, "run", id, "err", err.Error())
			continue
		}
		p.opts.Log.Info("the runs directory of a deleted object is removed", "kind", kind, "name", name, "run", id)
	}
}

func (r *cacheReader) runIDs(kinds ...workKind) map[string]bool {
	out := map[string]bool{}
	for _, kind := range kinds {
		for _, indexer := range r.kindIndexers(kind) {
			for _, obj := range indexer.List() {
				u, ok := obj.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				if id, _, _ := unstructured.NestedString(u.Object, "status", "runID"); id != "" {
					out[id] = true
				}
			}
		}
	}
	return out
}

func (p *Provider) keepRunsDir(ctx context.Context, reader *cacheReader, started time.Time) {
	keeper, ok := p.opts.PodRunner.(runsKeeper)
	if !ok {
		return
	}
	before := started
	var reapBefore time.Time
	t := time.NewTicker(RunsKeepEvery)
	defer t.Stop()
	for {
		named := reader.runIDs(workPod, workRun, workEngine)
		if !reapBefore.IsZero() {
			if reaped := keeper.Reap(func(id string) bool { return named[id] }, reapBefore); len(reaped) > 0 {
				p.opts.Log.Warn("live runs that no DenoPod, DenoRun or PolicyEngine names are stopped; a core adopts every run an object names and reaps the rest after a grace",
					"runs", reaped, "startedBefore", reapBefore.UTC().Format(time.RFC3339))
			}
		}
		if removed := keeper.Sweep(func(id string) bool { return named[id] }, before); len(removed) > 0 {
			p.opts.Log.Info("run directories that no DenoPod, DenoRun or PolicyEngine names are removed",
				"count", len(removed), "runs", removed, "lastActiveBefore", before.UTC().Format(time.RFC3339))
		}
		if rotated := keeper.CapLogs(RunLogCap); len(rotated) > 0 {
			p.opts.Log.Info("run logs over their cap were rotated; each keeps its newest bytes in a .1 copy", "files", rotated, "cap", RunLogCap)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		before = time.Now().Add(-RunDirKeep)
		reapBefore = time.Now().Add(-RunsKeepEvery)
	}
}
