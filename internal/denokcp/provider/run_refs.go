package provider

import (
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runref"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
)

const runRefTTL = 2 * time.Minute

// ponytail: recordRunRef runs the moment a workload starts, so the record exists before any later pass can read the run back; keepRunRef refreshes it once per pass and clears it on the cached phase rather than on the phase this pass computed, because the record has to outlive the terminal transition. A cached copy that still says Pending must keep finding it, or it would ask for a start the provider has already done. runRefTTL is the backstop for a run whose object is deleted before its status ever reads terminal.
func (p *Provider) recordRunRef(ref Ref, runID, uid string) {
	p.runRefs.Record(ref, runID, uid, p.opts.Now())
}

func (p *Provider) keepRunRef(ref Ref, runID, uid string, cachedTerminal bool) {
	p.runRefs.Keep(ref, runID, uid, cachedTerminal, p.opts.Now())
}

// ponytail: refuse to start a run this provider already started, so a cached copy that predates this pass's own start write cannot start a second workload. denorun is a stateless decider whose only memory is the status it is handed, so without this it asks to start again and the provider obeys: measured on the drain harness at 1000 runs and parallelism 20, 1062 starts for 1000 runs with 73 workloads live against a parallelism of 20. Two legitimate things must not be mistaken for staleness. A retry is one: denorun clears RunID on failure and returns the run to Pending, so Retries or StartTime in the cached object means the run has run before and is asking again. A recreated object behind the same name is the other, and it carries a new UID.
func (p *Provider) alreadyStarted(ref Ref, run *v1alpha1.DenoRun) bool {
	known, ok := p.runRefs.Lookup(ref)
	return runref.AlreadyStarted(known, ok, runref.Current{
		RunID:   run.Status.RunID,
		UID:     string(run.UID),
		Started: run.Status.StartTime != nil,
		Retries: run.Status.Retries,
	})
}

// ponytail: activeRuns counts workloads this provider started and has not yet seen reach a terminal phase, with the high-water mark kept for measurement. It is the gauge that found the duplicate starts above, which were invisible everywhere else: the API server only ever saw one object per run and the wall clock did not move.
func (p *Provider) noteRunActive() {
	p.runStarts.Add(1)
	n := p.activeRuns.Add(1)
	for {
		max := p.maxActiveRuns.Load()
		if n <= max || p.maxActiveRuns.CompareAndSwap(max, n) {
			return
		}
	}
}

func (p *Provider) noteRunInactive() {
	if p.activeRuns.Add(-1) < 0 {
		p.activeRuns.Store(0)
	}
}

func (p *Provider) ActiveRuns() int64 {
	return p.activeRuns.Load()
}

func (p *Provider) MaxActiveRuns() int64 {
	return p.maxActiveRuns.Load()
}

func (p *Provider) RunStarts() int64 {
	return p.runStarts.Load()
}
