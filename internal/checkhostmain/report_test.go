package checkhostmain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kernelmodules"
)

func TestOnlyMissingMakesThisExitNonZero(t *testing.T) {
	cases := []struct {
		name    string
		results []Result
		want    int
	}{
		{"nothing wrong", []Result{{Label: "a", Kind: Holds}, {Label: "b", Kind: PassedIn}}, 0},
		{"a host fact a container was not given", []Result{{Label: "a", Kind: PassedIn}}, 0},
		{"this environment is not what it says", []Result{{Label: "a", Kind: Holds}, {Label: "b", Kind: Missing}}, 1},
	}
	for _, tc := range cases {
		if got := exitCode(tc.results, false); got != tc.want {
			t.Fatalf("%s: exit code %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestABuildStepOnlyFailsOnWhatABuildReads(t *testing.T) {
	results := markBuildInputs([]Result{
		{Label: "host.kernel", Kind: Missing, Detail: "the kernel a build bakes is not here"},
		{Label: "host.postgres", Kind: Missing, Detail: "the tests' store is not here"},
	})
	if got := exitCode(results, true); got != 1 {
		t.Fatalf("a build step exited %d with a build input MISSING", got)
	}
	if !results[0].Build || results[1].Build {
		t.Fatalf("the build-input map marks host.kernel=%v and host.postgres=%v",
			results[0].Build, results[1].Build)
	}
	onlyTheTests := markBuildInputs([]Result{
		{Label: "host.postgres", Kind: Missing, Detail: "the tests' store is not here"},
	})
	if got := exitCode(onlyTheTests, true); got != 0 {
		t.Fatalf("a build step exited %d over a fact only the tests need", got)
	}
	if got := exitCode(onlyTheTests, false); got != 1 {
		t.Fatalf("a host run exited %d over a MISSING fact", got)
	}
}

func TestTheReportNamesTheCategoryOnItsOwnLine(t *testing.T) {
	var out bytes.Buffer
	code := writeReport(&out, []string{"host     x"}, []Result{
		{Label: "host.a", Kind: Holds, Detail: "it is here"},
		{Label: "host.b", Kind: Missing, Detail: "it is not here, and here is why: a very long " +
			"explanation that has to keep the category on the line it started on rather than " +
			"wrapping it away from the label a reader greps for"},
	}, false)
	if code != 1 {
		t.Fatalf("exit code %d, want 1: a MISSING line was in the report", code)
	}
	lines := strings.Split(out.String(), "\n")
	prefix := fmt.Sprintf("%-17s", Missing)
	grepped := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			grepped++
		}
	}
	if grepped != 1 {
		t.Fatalf("the report has %d lines starting with %q, want exactly 1:\n%s", grepped, prefix, out.String())
	}
	for _, line := range lines {
		if strings.Contains(line, "host.a") && !strings.Contains(line, string(Holds)) {
			t.Fatalf("the category is not on the line carrying the label: %q", line)
		}
	}
}

func TestTheHostsOwnNetworkEnumeratorIsWhatThePoolIsComparedAgainst(t *testing.T) {
	f := facts{}
	result := poolResult(f)
	if result.Label != "host.allocator-pool" {
		t.Fatalf("label %q", result.Label)
	}
	if result.Kind == "" {
		t.Fatal("the pool is reported with no category at all")
	}
	if !strings.Contains(result.Detail, "10.100.0.0/16") {
		t.Fatalf("the pool this project leases from is not named in the report: %s", result.Detail)
	}
	if !strings.Contains(result.Detail, "this host carries") && !strings.Contains(result.Detail, "no networks at all") {
		t.Fatalf("the host's own networks are not reported: %s", result.Detail)
	}
}

func TestAnExtensionWithNoNamedProviderIsRefusedAgainstTheStagedTree(t *testing.T) {
	stage := t.TempDir()
	tree := filepath.Join(stage, "lib", "modules", "1.2.3")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatalf("staging the module tree: %v", err)
	}
	bodies := map[string]string{
		"modules.dep":     "kernel/net/netfilter/nf_conntrack.ko:\nkernel/net/netfilter/xt_nat.ko:\n",
		"modules.builtin": "\n",
		"modules.alias":   "alias ipt_DNAT xt_nat\n",
	}
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(tree, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	f := facts{
		release:      "1.2.3",
		moduleSource: "/lib/modules/1.2.3",
		stageDir:     stage,
		modules:      []string{"nf_conntrack"},
	}
	if got := modulesResolveResult(f).Kind; got != Missing {
		t.Fatalf("a config whose guest would reach for DNAT with no module named for it reported %q: "+
			"the staged tree is what the guest boots from and it is what the check has to read", got)
	}
	f.modules = []string{"nf_conntrack", "xt_nat"}
	if got := modulesResolveResult(f).Kind; got != Holds {
		t.Fatalf("a config naming every provider the staged tree carries reported %q", got)
	}
}

func TestTheRecordedModuleListIsTheBuildersOwnCheck(t *testing.T) {
	source := t.TempDir()
	for name, body := range map[string]string{
		"modules.dep":     "kernel/net/netfilter/xt_nat.ko:\n",
		"modules.builtin": "\n",
		"modules.alias":   "alias ipt_DNAT xt_nat\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	err := kernelmodules.Verify(source, []string{})
	if err == nil {
		t.Fatal("the builder's own check accepted a list naming neither DNAT's provider nor its own " +
			"built-ins: the guest would reach for an extension nothing on its boot path loads")
	}
	if !strings.Contains(err.Error(), "DNAT") || !strings.Contains(err.Error(), "xt_nat") {
		t.Fatalf("the refusal does not name the extension and the module that provides it: %v", err)
	}
	if err := kernelmodules.Verify(source, []string{"xt_nat"}); err != nil {
		t.Fatalf("the builder's own check refused a list naming the provider the tree carries: %v", err)
	}
}

func TestAPathTheOperatorDidNotNameIsNotASilentDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodeimage.json")
	if err := os.WriteFile(path, []byte(`{"kernel_source": "/boot/vmlinuz-x", "modules": ["nf_conntrack"]}`), 0o644); err != nil {
		t.Fatalf("writing a config that names two keys of the nine: %v", err)
	}
	_, absent, err := loadNodeImageConfig(path)
	if err != nil {
		t.Fatalf("loadNodeImageConfig: %v", err)
	}
	if len(absent) != len(configKeys)-2 {
		t.Fatalf("a config naming two of the %d keys reported %d absent: %v",
			len(configKeys), len(absent), absent)
	}
	for _, key := range []string{"kernel_source", "modules"} {
		for _, got := range absent {
			if got == key {
				t.Fatalf("%s is in the file and reported absent", key)
			}
		}
	}
}
