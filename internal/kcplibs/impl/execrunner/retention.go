package execrunner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/logcap"
)

const (
	StdoutFile = "stdout.txt"
	StderrFile = "stderr.txt"
)

func (p *Pod) Remove(_ context.Context, runID string) error { return p.sup.remove(runID) }

func (e *Engine) Remove(_ context.Context, runID string) error { return e.sup.remove(runID) }

func (p *Pod) Sweep(named func(string) bool, before time.Time) []string {
	return SweepRuns(p.opts.RunsDir, named, before)
}

func (p *Pod) CapLogs(capBytes int64) []string { return CapRunLogs(p.opts.RunsDir, capBytes) }

func lastActive(dir string) time.Time {
	var last time.Time
	if st, err := os.Stat(dir); err == nil {
		last = st.ModTime()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return last
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
	}
	return last
}

func SweepRuns(runsDir string, named func(string) bool, before time.Time) []string {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil
	}
	live := liveRuns(runsDir)
	var removed []string
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || named(id) {
			continue
		}
		if _, ok := live[id]; ok {
			continue
		}
		dir := filepath.Join(runsDir, id)
		if !lastActive(dir).Before(before) {
			continue
		}
		if os.RemoveAll(dir) == nil {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	return removed
}

func CapRunLogs(runsDir string, capBytes int64) []string {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil
	}
	var rotated []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, name := range []string{StdoutFile, StderrFile} {
			p := filepath.Join(runsDir, e.Name(), name)
			if ok, _ := logcap.Rotate(p, capBytes); ok {
				rotated = append(rotated, filepath.Join(e.Name(), name))
			}
		}
	}
	return rotated
}
