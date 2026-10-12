package probe

import "testing"

func TestFailureThreshold(t *testing.T) {
	tracker := NewTracker()
	for i := 1; i <= 2; i++ {
		if tracker.Record("pod", "pod-1", false, 3) {
			t.Fatalf("failure %d is below the threshold", i)
		}
	}
	if !tracker.Record("pod", "pod-1", false, 3) {
		t.Fatal("the third failure must trip the threshold")
	}
	if tracker.Record("pod", "pod-1", true, 3) {
		t.Fatal("a success resets the counter")
	}
	if tracker.Record("pod", "pod-1", false, 3) {
		t.Fatal("the count restarted after the success")
	}
}

func TestANewRunResetsTheCounter(t *testing.T) {
	tracker := NewTracker()
	tracker.Record("pod", "pod-1", false, 3)
	tracker.Record("pod", "pod-1", false, 3)
	if tracker.Record("pod", "pod-2", false, 3) {
		t.Fatal("a restarted workload starts from zero")
	}
}

func TestDefaultsAndForget(t *testing.T) {
	if EffectiveThreshold(0) != DefaultFailureThreshold {
		t.Fatal("an unset threshold falls back to the default")
	}
	if EffectiveThreshold(7) != 7 {
		t.Fatal("an explicit threshold is preserved")
	}
	tracker := NewTracker()
	tracker.Record("pod", "pod-1", false, 1)
	tracker.Forget("pod")
	if tracker.Len() != 0 {
		t.Fatal("forget must drop the counter")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	tracker := NewTracker()
	tracker.Record("pod", "run-1", false, 2)
	if tracker.Record("engine", "run-1", false, 2) {
		t.Fatal("a different key must count separately")
	}
}
