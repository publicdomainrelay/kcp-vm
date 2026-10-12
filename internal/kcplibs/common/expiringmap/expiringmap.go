package expiringmap

import (
	"sync"
	"time"
)

type Entry[V any] struct {
	Value V

	At time.Time
}

type Map[K comparable, V any] struct {
	TTL time.Duration

	mu sync.Mutex

	entries map[K]Entry[V]
}

func NewMap[K comparable, V any](ttl time.Duration) *Map[K, V] {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Map[K, V]{TTL: ttl, entries: map[K]Entry[V]{}}
}

func (m *Map[K, V]) Set(key K, value V, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = Entry[V]{Value: value, At: at}
}

func (m *Map[K, V]) Get(key K, at time.Time) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	if m.expired(entry, at) {
		delete(m.entries, key)
		var zero V
		return zero, false
	}
	return entry.Value, true
}

func (m *Map[K, V]) Delete(key K) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
}

func (m *Map[K, V]) DeleteIf(drop func(K, V) bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropLocked(func(key K, entry Entry[V]) bool {
		return drop(key, entry.Value)
	})
}

func (m *Map[K, V]) Expire(at time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropLocked(func(_ K, entry Entry[V]) bool {
		return m.expired(entry, at)
	})
}

func (m *Map[K, V]) Range(visit func(K, V) bool) {
	m.mu.Lock()
	snapshot := make(map[K]V, len(m.entries))
	for key, entry := range m.entries {
		snapshot[key] = entry.Value
	}
	m.mu.Unlock()
	for key, value := range snapshot {
		if !visit(key, value) {
			return
		}
	}
}

func (m *Map[K, V]) SetPruning(key K, value V, at time.Time, prune func(K, V) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for candidate, entry := range m.entries {
		if m.expired(entry, at) || (prune != nil && prune(candidate, entry.Value)) {
			delete(m.entries, candidate)
		}
	}
	m.entries[key] = Entry[V]{Value: value, At: at}
}

func (m *Map[K, V]) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func (m *Map[K, V]) expired(entry Entry[V], at time.Time) bool {
	return at.Sub(entry.At) >= m.TTL
}

func (m *Map[K, V]) dropLocked(drop func(K, Entry[V]) bool) int {
	dropped := 0
	for key, entry := range m.entries {
		if drop(key, entry) {
			delete(m.entries, key)
			dropped++
		}
	}
	return dropped
}

const DefaultTTL = 2 * time.Minute
