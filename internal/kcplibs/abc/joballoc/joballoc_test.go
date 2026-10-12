package joballoc

import (
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

func TestAllocateAndNames(t *testing.T) {
	allocator := New(time.Minute)
	job := ref.New("root:alice", "default", "job-1")
	now := time.Unix(1000, 0)
	allocator.Allocate(job, []string{"job-1-1", "job-1-2"}, now)
	if names := allocator.Names(job, now); len(names) != 2 {
		t.Fatalf("names = %v", names)
	}
	allocator.Allocate(job, []string{"job-1-3"}, now)
	if names := allocator.Names(job, now); len(names) != 3 {
		t.Fatalf("names = %v", names)
	}
	if allocator.Names(ref.New("root:alice", "default", "other"), now) != nil {
		t.Fatal("another job has no allocations")
	}
}

func TestPendingDropsObservedAndExpired(t *testing.T) {
	allocator := New(time.Minute)
	job := ref.New("root:alice", "default", "job-1")
	now := time.Unix(1000, 0)
	allocator.Allocate(job, []string{"job-1-1", "job-1-2"}, now)
	pending := allocator.Pending(job, []string{"job-1-1"}, now)
	if len(pending) != 1 || pending[0] != "job-1-2" {
		t.Fatalf("pending = %v", pending)
	}
	if pending := allocator.Pending(job, nil, time.Unix(1000+61, 0)); pending != nil {
		t.Fatalf("expired allocations = %v", pending)
	}
}

func TestNamesDropsExpiredAllocations(t *testing.T) {
	allocator := New(time.Minute)
	job := ref.New("root:alice", "default", "job-1")
	now := time.Unix(1000, 0)
	allocator.Allocate(job, []string{"job-1-1"}, now)
	if names := allocator.Names(job, time.Unix(1000+61, 0)); names != nil {
		t.Fatalf("expired names = %v, want none", names)
	}
}

func TestForgetAndLen(t *testing.T) {
	allocator := New(time.Minute)
	job := ref.New("root:alice", "default", "job-1")
	allocator.Allocate(job, []string{"job-1-1"}, time.Unix(1000, 0))
	if allocator.Len() != 1 {
		t.Fatalf("jobs = %d", allocator.Len())
	}
	allocator.Forget(job)
	if allocator.Len() != 0 {
		t.Fatal("forget must drop the job")
	}
	allocator.Allocate(job, nil, time.Unix(1000, 0))
	if allocator.Len() != 0 {
		t.Fatal("allocating nothing must not create an entry")
	}
}

func TestMergeNamesKeepsFirstOccurrenceOrder(t *testing.T) {
	merged := MergeNames([]string{"a", "b", ""}, []string{"b", "c"})
	if len(merged) != 3 || merged[0] != "a" || merged[1] != "b" || merged[2] != "c" {
		t.Fatalf("merged = %v", merged)
	}
	if MergeNames(nil, nil) != nil && len(MergeNames(nil, nil)) != 0 {
		t.Fatal("merging nothing yields nothing")
	}
}
