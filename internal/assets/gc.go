package assets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type UnitRef struct {
	Backend string
	Name    string
	Exec    []string
	Env     []string
}

type UnitLister interface {
	LoadedUnits(ctx context.Context) ([]UnitRef, error)
}

type UnitListerFunc func(ctx context.Context) ([]UnitRef, error)

func (f UnitListerFunc) LoadedUnits(ctx context.Context) ([]UnitRef, error) { return f(ctx) }

type GCPolicy struct {
	Keep   []string
	Window time.Duration
	Now    time.Time
}

type GCKept struct {
	Dir    string `json:"dir"`
	Reason string `json:"reason"`
}

type GCReport struct {
	Root    string   `json:"root"`
	Removed []string `json:"removed"`
	Kept    []GCKept `json:"kept"`
}

func Collect(ctx context.Context, root string, listers []UnitLister, p GCPolicy) (GCReport, error) {
	rep := GCReport{Root: root, Removed: []string{}, Kept: []GCKept{}}
	if !filepath.IsAbs(root) {
		return rep, fmt.Errorf("assets: gc root %q is not absolute", root)
	}
	if p.Now.IsZero() {
		p.Now = time.Now()
	}
	var units []UnitRef
	for _, l := range listers {
		u, err := l.LoadedUnits(ctx)
		if err != nil {
			return rep, fmt.Errorf("assets: gc refuses: the loaded units cannot be read: %w", err)
		}
		units = append(units, u...)
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	keep := map[string]bool{}
	for _, k := range p.Keep {
		keep[k] = true
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		scratch := strings.HasPrefix(name, tmpPrefix) || strings.HasPrefix(name, stalePrefix)
		if !e.IsDir() || (!digestName.MatchString(name) && !scratch) {
			continue
		}
		dir := filepath.Join(root, name)
		if keep[name] {
			rep.Kept = append(rep.Kept, GCKept{Dir: dir, Reason: "the current or previous binary's asset set"})
			continue
		}
		if u, ok := referencedBy(dir, units); ok {
			rep.Kept = append(rep.Kept, GCKept{Dir: dir, Reason: fmt.Sprintf("named by loaded %s unit %s", u.Backend, u.Name)})
			continue
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return rep, err
		}
		if age := p.Now.Sub(info.ModTime()); age < p.Window {
			rep.Kept = append(rep.Kept, GCKept{Dir: dir, Reason: fmt.Sprintf("used %s ago, inside the %s rollback window", age.Round(time.Second), p.Window)})
			continue
		}
		if err := removeTree(dir); err != nil {
			return rep, err
		}
		os.Remove(filepath.Join(root, lockPrefix+name))
		rep.Removed = append(rep.Removed, dir)
	}
	return rep, nil
}

func IsDevsup(argv []string) bool {
	return len(argv) >= 3 && argv[1] == "internal" && argv[2] == "devsup"
}

func DevCollect(ctx context.Context, root string, devsups []int, listers []UnitLister, p GCPolicy) (GCReport, error) {
	if len(devsups) > 0 {
		return GCReport{Root: root, Removed: []string{}, Kept: []GCKept{}},
			fmt.Errorf("assets: dev gc refuses while a devsup of this user runs (pids %v); the per-user cache is shared by every dev instance, run dev down first", devsups)
	}
	return Collect(ctx, root, listers, p)
}

func referencedBy(dir string, units []UnitRef) (UnitRef, bool) {
	for _, u := range units {
		for _, s := range u.Exec {
			if names(s, dir) {
				return u, true
			}
		}
		for _, s := range u.Env {
			if _, v, ok := strings.Cut(s, "="); ok && names(v, dir) {
				return u, true
			}
		}
	}
	return UnitRef{}, false
}

func names(s, dir string) bool {
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == '=' || r == ' ' || r == ',' }) {
		c := filepath.Clean(part)
		if c == dir || strings.HasPrefix(c, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
