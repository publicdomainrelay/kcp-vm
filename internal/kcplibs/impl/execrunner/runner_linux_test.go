//go:build linux

package execrunner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

const unitStub = `printf 'SWC_UNIT=%s\n' "$SWC_UNIT" > "$PROBE_OUT.tmp" && mv "$PROBE_OUT.tmp" "$PROBE_OUT"`

func TestEveryDenoPodProcessCarriesTheCoreUnitName(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	pod, err := NewPod(PodOptions{
		DenoBin:  writeStub(t, dir, unitStub),
		RunsDir:  filepath.Join(dir, "runs"),
		ExtraEnv: []string{"PROBE_OUT=" + out},
		Unit:     "socialweb-core.service",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x", Env: map[string]string{UnitEnv: "socialweb-other.service"}})
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if got := envSeen(t, out)[UnitEnv]; got != "socialweb-core.service" {
		t.Fatalf("%s = %q in the DenoPod process; devsup kills a process that left the core's group only when it names the core unit", UnitEnv, got)
	}
}

func TestWithoutAUnitTheProcessInheritsTheCoresOwn(t *testing.T) {
	t.Setenv(UnitEnv, "socialweb-inherited.service")
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	pod, err := NewPod(PodOptions{
		DenoBin:  writeStub(t, dir, unitStub),
		RunsDir:  filepath.Join(dir, "runs"),
		ExtraEnv: []string{"PROBE_OUT=" + out},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if got := envSeen(t, out)[UnitEnv]; got != "socialweb-inherited.service" {
		t.Fatalf("%s = %q; with no Unit option the core's own attribution must reach the DenoPod process", UnitEnv, got)
	}
}
