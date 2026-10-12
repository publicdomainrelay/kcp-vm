package denocomputer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var assignment = regexp.MustCompile(`^\s*(\w+)(?:\s+\w+)?\s*=\s*"([^"]+)"`)

func consumerConstants(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(root, "internal", "denokcp")
		if info, err := os.Stat(filepath.Join(candidate, "api", "v1alpha1")); err == nil && info.IsDir() {
			out := readConstants(t, filepath.Join(candidate, "api", "v1alpha1"))
			for name, value := range readConstants(t, filepath.Join(candidate, "provider")) {
				out[name] = value
			}
			return out
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	if os.Getenv("KCP_LIBS_REQUIRE_CONSUMER") == "1" {
		t.Fatal("the consumer's api/v1alpha1 was not found; this test reads it as the source of truth, so set KCP_LIBS_REQUIRE_CONSUMER only where the checkout is present")
	}
	t.Skip("the consumer checkout is not next to this module, so there is nothing to compare against")
	return nil
}

func readConstants(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			match := assignment.FindStringSubmatch(strings.TrimPrefix(line, "const "))
			if match == nil {
				continue
			}
			out[match[1]] = match[2]
		}
	}
	return out
}

func TestTheGroupIsTheOneTheConsumerDeclares(t *testing.T) {
	declared := consumerConstants(t)
	want, ok := declared["GroupName"]
	if !ok {
		t.Fatal("the consumer declares no GroupName, so the group this module uses cannot be checked against it")
	}
	if Group != want {
		t.Fatalf("Group = %q, the consumer declares %q", Group, want)
	}
	version, ok := declared["Version"]
	if !ok {
		t.Fatal("the consumer declares no Version")
	}
	if Version != version {
		t.Fatalf("Version = %q, the consumer declares %q", Version, version)
	}
}

func TestEveryFinalizerIsOneTheConsumerDeclares(t *testing.T) {
	declared := consumerConstants(t)
	for name, value := range map[string]string{
		"FinalizerDenoRun":           FinalizerDenoRun,
		"FinalizerDenoPod":           FinalizerDenoPod,
		"FinalizerPolicyEngine":      FinalizerPolicyEngine,
		"FinalizerPolicyWorkflowRun": FinalizerPolicyWorkflowRun,
		"FinalizerOpenBao":           FinalizerOpenBao,
	} {
		want, ok := declared[name]
		if !ok {
			t.Fatalf("the consumer declares no %s, so this one is invented", name)
		}
		if value != want {
			t.Fatalf("%s = %q, the consumer declares %q", name, value, want)
		}
	}
}

func TestTheLabelsAreTheOnesTheConsumerDeclares(t *testing.T) {
	declared := consumerConstants(t)
	for name, value := range map[string]string{
		"PolicyWorkflowPodLabel": PolicyWorkflowPodLabel,
		"JobRunLabel":            JobRunLabel,
	} {
		want, ok := declared[name]
		if !ok {
			t.Fatalf("the consumer declares no %s, so this module's value for it is invented", name)
		}
		if value != want {
			t.Fatalf("%s = %q, the consumer declares %q", name, value, want)
		}
	}
	if want := declared["TriggerLabel"]; want != "" {
		if TriggerLabel != want {
			t.Fatalf("TriggerLabel = %q, the consumer declares %q", TriggerLabel, want)
		}
		return
	}
	if !consumerMentions(t, fmt.Sprintf("%q", TriggerLabel)) {
		t.Fatalf("the consumer never names %q, so this module's value for it is invented", TriggerLabel)
	}
}

func consumerMentions(t *testing.T, needle string) bool {
	t.Helper()
	root := consumerRoot(t)
	if root == "" {
		return false
	}
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(body), needle) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func consumerRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(root, "internal", "denokcp", "api", "v1alpha1")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return filepath.Join(root, "internal", "denokcp")
		}
		parent := filepath.Dir(root)
		if parent == root {
			return ""
		}
		root = parent
	}
}

func TestThePhasesAndConditionsAreTheOnesTheConsumerStores(t *testing.T) {
	declared := consumerConstants(t)
	for name, value := range map[string]string{
		"DenoRunPending":            string(PhasePending),
		"DenoRunRunning":            string(PhaseRunning),
		"DenoRunSucceeded":          string(PhaseSucceeded),
		"DenoRunFailed":             string(PhaseFailed),
		"PolicyWorkflowCancelled":   string(PhaseCancelled),
		"RunTriggerTriggered":       string(PhaseTriggered),
		"ConditionReady":            ConditionReady,
		"ConditionComplete":         ConditionComplete,
		"ConditionFailed":           ConditionFailed,
		"ConditionSuspended":        ConditionSuspended,
		"ConditionCancelled":        ConditionCancelled,
		"OpenBaoConditionReady":     OpenBaoConditionReady,
		"OpenBaoConditionAmbiguous": OpenBaoConditionAmbiguous,
	} {
		want, ok := declared[name]
		if !ok {
			t.Fatalf("the consumer declares no %s, so this module's value for it is invented", name)
		}
		if value != want {
			t.Fatalf("%s = %q, the consumer declares %q", name, value, want)
		}
	}
	for name, reason := range map[string]string{
		"ReasonPolicyWorkflowPodMissing": ReasonPolicyWorkflowPodMissing,
		"ReasonEngineNotReady":           ReasonEngineNotReady,
		"ReasonQueued":                   ReasonQueued,
	} {
		if !consumerMentions(t, fmt.Sprintf("%q", reason)) {
			t.Fatalf("%s = %q, but the consumer never writes that literal, so %s is invented", name, reason, name)
		}
	}
}

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
