package checkhostmain

import (
	"fmt"
	"io"
	"strings"
)

type Kind string

const (
	Holds         Kind = "HOLDS"
	Missing       Kind = "MISSING"
	PassedIn      Kind = "MUST BE PASSED IN"
	NotApplicable Kind = "NOT APPLICABLE"
)

type Result struct {
	Label string

	Kind Kind

	Detail string

	Build bool
}

var buildInputs = map[string]bool{
	"host.config":          true,
	"host.kernel":          true,
	"host.kernel-config":   true,
	"host.module-tree":     true,
	"host.modules-resolve": true,
	"host.k3s":             true,
	"host.disk":            true,
	"host.selinux":         true,
}

func markBuildInputs(results []Result) []Result {
	for i := range results {
		results[i].Build = buildInputs[results[i].Label]
	}
	return results
}

func (r Result) write(w io.Writer) {
	fmt.Fprintf(w, "%-17s %s\n", r.Kind, r.Label)
	for _, line := range wrap(r.Detail, 92) {
		fmt.Fprintf(w, "%-17s %s\n", "", line)
	}
}

func wrap(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	lines := []string{}
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}

func count(results []Result, kind Kind) int {
	n := 0
	for _, r := range results {
		if r.Kind == kind {
			n++
		}
	}
	return n
}

func exitCode(results []Result, buildOnly bool) int {
	for _, r := range results {
		if r.Kind != Missing {
			continue
		}
		if buildOnly && !r.Build {
			continue
		}
		return 1
	}
	return 0
}

func writeReport(w io.Writer, header []string, results []Result, buildOnly bool) int {
	fmt.Fprintln(w, "socialweb-computer-kcp-check-host: the host facts this project's proofs are made of")
	fmt.Fprintln(w)
	for _, line := range header {
		fmt.Fprintln(w, "  "+line)
	}
	if buildOnly {
		fmt.Fprintln(w, "  mode     a build step, so only the checks a build READS can fail this run: "+
			"a host fact the tests need and an image build does not is reported and does not stop it")
	}
	fmt.Fprintln(w)
	for _, r := range results {
		r.write(w)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%d checks: %d HOLDS, %d MISSING, %d MUST BE PASSED IN\n",
		len(results), count(results, Holds), count(results, Missing), count(results, PassedIn))
	fmt.Fprintf(w, "%s\n", categoryLegend)
	return exitCode(results, buildOnly)
}

const categoryLegend = "MISSING is this environment failing to be what it says it is, and it is the " +
	"only category that makes this exit non-zero. MUST BE PASSED IN is what a container cannot have " +
	"of its own -- a host's kernel, the module tree built beside it, /dev/kvm, the permission to " +
	"create a user namespace, a VMM binary and an image built from that kernel -- so it has to be " +
	"handed to one. Which of the two a missing host fact is therefore depends on WHERE THIS RUNS, " +
	"and the header says which: run on a host, the host is the thing that is supposed to provide " +
	"it, so a missing kernel or module tree is MISSING and this exits non-zero; run in a container " +
	"that has not been handed them, the same lines are MUST BE PASSED IN, which is the list of " +
	"things the container needs and the same list a build reads to see it was handed everything " +
	"before it writes an image"
