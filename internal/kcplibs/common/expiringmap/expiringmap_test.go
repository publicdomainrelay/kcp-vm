package expiringmap

import (
	"sort"
	"testing"
	"time"
)

func TestSetGetAndExpiry(t *testing.T) {
	entries := NewMap[string, int](time.Minute)
	now := time.Unix(1000, 0)
	entries.Set("a", 1, now)
	if value, ok := entries.Get("a", now); !ok || value != 1 {
		t.Fatalf("get = (%d, %v)", value, ok)
	}
	if _, ok := entries.Get("missing", now); ok {
		t.Fatal("a missing key must not be found")
	}
	if _, ok := entries.Get("a", now.Add(time.Minute)); ok {
		t.Fatal("an entry at its ttl must be gone")
	}
	if entries.Len() != 0 {
		t.Fatalf("entries = %d, want the expired one dropped", entries.Len())
	}
}

func TestDefaultTTLWhenUnset(t *testing.T) {
	if NewMap[string, int](0).TTL != DefaultTTL {
		t.Fatal("a non-positive ttl falls back to the default")
	}
	if NewMap[string, int](-time.Second).TTL != DefaultTTL {
		t.Fatal("a negative ttl falls back to the default")
	}
}

func TestDeleteAndDeleteIf(t *testing.T) {
	entries := NewMap[string, int](time.Minute)
	now := time.Unix(1000, 0)
	entries.Set("a", 1, now)
	entries.Set("b", 2, now)
	entries.Set("c", 3, now)

	if dropped := entries.DeleteIf(func(_ string, value int) bool { return value == 2 }); dropped != 1 {
		t.Fatalf("dropped = %d", dropped)
	}
	entries.Delete("a")
	if entries.Len() != 1 {
		t.Fatalf("entries = %d", entries.Len())
	}
}

func TestExpireSweepsOnlyWhatIsPastItsTTL(t *testing.T) {
	entries := NewMap[string, int](time.Minute)
	start := time.Unix(1000, 0)
	entries.Set("old", 1, start)
	entries.Set("new", 2, start.Add(50*time.Second))
	if dropped := entries.Expire(start.Add(70 * time.Second)); dropped != 1 {
		t.Fatalf("dropped = %d, want only the old entry", dropped)
	}
	if _, ok := entries.Get("new", start.Add(70*time.Second)); !ok {
		t.Fatal("the fresh entry must survive")
	}
}

func TestRangeVisitsEveryEntry(t *testing.T) {
	entries := NewMap[string, int](time.Minute)
	now := time.Unix(1000, 0)
	entries.Set("a", 1, now)
	entries.Set("b", 2, now)
	var keys []string
	entries.Range(func(key string, _ int) bool {
		keys = append(keys, key)
		return true
	})
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys = %v", keys)
	}
	entries.Range(func(string, int) bool { return false })
	if entries.Len() != 2 {
		t.Fatal("stopping a range must not change the map")
	}
}
