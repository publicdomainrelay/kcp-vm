//go:build linux

package execrunner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/listeners"
)

const listenersStub = `printf 'LISTENERS_FILE=%s\n' "$LISTENERS_FILE" > "$PROBE_OUT.tmp" && mv "$PROBE_OUT.tmp" "$PROBE_OUT"
if [ -n "$REPORT" ]; then printf '%s' "$REPORT" > "$LISTENERS_FILE"; fi
while [ ! -e "$RELEASE" ]; do sleep 0.02; done`

func startListenersStub(t *testing.T, extra ...string) (*Pod, string, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	release := filepath.Join(dir, "release")
	stub := writeStub(t, dir, listenersStub)
	env := append([]string{"PROBE_OUT=" + out, "RELEASE=" + release}, extra...)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs"), ExtraEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o644)
		observeUntilDone(t, pod, id)
	})
	waitForFile(t, out)
	return pod, id, out
}

func TestPodPointsListenersFileAtItsRunDirectory(t *testing.T) {
	pod, id, out := startListenersStub(t)
	want := filepath.Join(pod.opts.RunsDir, id, listeners.FileName)
	if seen := envSeen(t, out); seen[listeners.FileEnv] != want {
		t.Fatalf("%s = %q, want %q: a workload reports its listeners beside its own run", listeners.FileEnv, seen[listeners.FileEnv], want)
	}
}

func TestPodReportsEveryListenerItsProcessWrote(t *testing.T) {
	report := `{"listeners":[{"name":"http","kind":"tcp","address":"127.0.0.1:41001"},{"name":"admin","kind":"tcp","address":"0.0.0.0:41002"},{"name":"control","kind":"unix","address":"/run/pds/control.sock"}]}`
	pod, id, _ := startListenersStub(t, "REPORT="+report)
	want := []listeners.Listener{
		{Name: "http", Kind: listeners.KindTCP, Address: "127.0.0.1:41001"},
		{Name: "admin", Kind: listeners.KindTCP, Address: "127.0.0.1:41002"},
		{Name: "control", Kind: listeners.KindUnix, Address: "/run/pds/control.sock"},
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := pod.Observe(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(status.Listeners, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Observe never reported the three listeners the process wrote to its listeners file")
}

func TestPodReportsNoListenerFromATruncatedReport(t *testing.T) {
	pod, id, _ := startListenersStub(t, `REPORT={"listeners":[{"name":"http"`)
	time.Sleep(100 * time.Millisecond)
	status, err := pod.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Listeners) != 0 {
		t.Fatalf("Listeners = %+v from a report that does not parse", status.Listeners)
	}
}
