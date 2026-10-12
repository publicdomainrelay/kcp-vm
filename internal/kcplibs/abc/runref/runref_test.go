package runref

import (
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/ref"
)

func TestRecordAndLookupByRef(t *testing.T) {
	index := New(time.Minute)
	target := ref.New("root:alice", "default", "run-1")
	now := time.Unix(1000, 0)
	index.Record(target, "pod-1", "uid-1", now)

	entry, found := index.Lookup(target)
	if !found || entry.RunID != "pod-1" || entry.UID != "uid-1" {
		t.Fatalf("lookup = (%+v, %v)", entry, found)
	}
	if index.Len() != 1 {
		t.Fatalf("entries = %d", index.Len())
	}
}

func TestRecordReplacesAnEarlierStartOfTheSameRef(t *testing.T) {
	index := New(time.Minute)
	target := ref.New("root:alice", "default", "run-1")
	now := time.Unix(1000, 0)
	index.Record(target, "pod-1", "uid-1", now)
	index.Record(target, "pod-2", "uid-1", now)
	if index.Len() != 1 {
		t.Fatalf("entries = %d, want the run to keep one start", index.Len())
	}
	entry, _ := index.Lookup(target)
	if entry.RunID != "pod-2" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestEntriesExpireAndTerminalRunsForget(t *testing.T) {
	index := New(time.Minute)
	target := ref.New("root:alice", "default", "run-1")
	index.Record(target, "pod-1", "uid-1", time.Unix(1000, 0))
	index.Record(target, "pod-1", "uid-1", time.Unix(1000+61, 0))
	if index.Len() != 1 {
		t.Fatalf("entries = %d, want the expired entry swept", index.Len())
	}
	index.Keep(target, "pod-1", "uid-1", true, time.Unix(1000+61, 0))
	if index.Len() != 0 {
		t.Fatal("a terminal run must not keep a start record")
	}
}

func TestAlreadyStartedRefusesAStalePendingCopy(t *testing.T) {
	known := Record{RunID: "pod-1", UID: "uid-1"}
	if !AlreadyStarted(known, true, Current{}) {
		t.Fatal("a cached Pending copy must not start a second workload")
	}
	if AlreadyStarted(known, false, Current{}) {
		t.Fatal("no record means no refusal")
	}
	if AlreadyStarted(known, true, Current{RunID: "pod-1"}) {
		t.Fatal("the same workload is not a duplicate")
	}
	if AlreadyStarted(known, true, Current{Started: true}) {
		t.Fatal("a run that has started before is retrying, not duplicating")
	}
	if AlreadyStarted(known, true, Current{Retries: 1}) {
		t.Fatal("a retry is not a duplicate")
	}
	if AlreadyStarted(known, true, Current{UID: "uid-2"}) {
		t.Fatal("a recreated object behind the same name is not a duplicate")
	}
}
