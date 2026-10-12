package joballoc

import (
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/expiringmap"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

type Allocation struct {
	Name string

	At time.Time
}

type Allocator struct {
	byJob *expiringmap.Map[ref.Ref, []Allocation]
}

func New(ttl time.Duration) *Allocator {
	return &Allocator{byJob: expiringmap.NewMap[ref.Ref, []Allocation](ttl)}
}

func (a *Allocator) Allocate(r ref.Ref, names []string, now time.Time) {
	if len(names) == 0 {
		return
	}
	kept, _ := a.byJob.Get(r, now)
	for _, name := range names {
		kept = append(kept, Allocation{Name: name, At: now})
	}
	a.byJob.Set(r, kept, now)
}

func (a *Allocator) Names(r ref.Ref, now time.Time) []string {
	allocations, ok := a.byJob.Get(r, now)
	if !ok || len(allocations) == 0 {
		return nil
	}
	out := make([]string, 0, len(allocations))
	for _, allocation := range allocations {
		out = append(out, allocation.Name)
	}
	return out
}

func (a *Allocator) Pending(r ref.Ref, observed []string, now time.Time) []string {
	allocations, ok := a.byJob.Get(r, now)
	if !ok {
		return nil
	}
	seen := make(map[string]bool, len(observed))
	for _, name := range observed {
		seen[name] = true
	}
	var out []string
	for _, allocation := range allocations {
		if seen[allocation.Name] {
			continue
		}
		seen[allocation.Name] = true
		out = append(out, allocation.Name)
	}
	return out
}

func (a *Allocator) Forget(r ref.Ref) {
	a.byJob.Delete(r)
}

func (a *Allocator) Len() int {
	return a.byJob.Len()
}

func MergeNames(groups ...[]string) []string {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	seen := make(map[string]bool, total)
	out := make([]string, 0, total)
	for _, group := range groups {
		for _, name := range group {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
