package denocomputer

import (
	"testing"
)

func TestTheTerminalPredicatesSeparateTheKinds(t *testing.T) {
	for name, terminal := range map[string]func(string) bool{
		"TerminalDenoRun": TerminalDenoRun,
		"TerminalDenoPod": TerminalDenoPod,
		"TerminalDenoJob": TerminalDenoJob,
	} {
		if !terminal(string(PhaseSucceeded)) || !terminal(string(PhaseFailed)) {
			t.Fatalf("%s must hold for both finished phases", name)
		}
		if terminal(string(PhaseRunning)) || terminal(string(PhasePending)) {
			t.Fatalf("%s must not hold for a run in flight", name)
		}
	}
	if !TerminalPolicyWorkflow(string(PhaseCancelled)) {
		t.Fatal("a cancelled policy run is terminal")
	}
	if TerminalPolicyEngine(string(PhaseRunning)) || !TerminalPolicyEngine(string(PhaseFailed)) {
		t.Fatal("a policy engine is terminal only when it has failed")
	}
	if !RunningPhase(string(PhaseRunning)) || RunningPhase(string(PhaseSucceeded)) {
		t.Fatal("only a running run is running")
	}
}
