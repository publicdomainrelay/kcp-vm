package probe

import "sync"

const DefaultFailureThreshold int32 = 3

type Counter struct {
	RunID string

	Failures int
}

type Tracker struct {
	mu sync.Mutex

	counters map[string]Counter
}

func NewTracker() *Tracker {
	return &Tracker{counters: map[string]Counter{}}
}

func (t *Tracker) Record(key, runID string, passed bool, threshold int32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	counter, seen := t.counters[key]
	if !seen || counter.RunID != runID {
		counter = Counter{RunID: runID}
	}
	if passed {
		counter.Failures = 0
	} else {
		counter.Failures++
	}
	t.counters[key] = counter
	return int32(counter.Failures) >= EffectiveThreshold(threshold)
}

func (t *Tracker) Forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counters, key)
}

func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.counters)
}

func EffectiveThreshold(threshold int32) int32 {
	if threshold > 0 {
		return threshold
	}
	return DefaultFailureThreshold
}
