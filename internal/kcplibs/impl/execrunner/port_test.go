//go:build linux

package execrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

const portStub = `printf 'PORT_FILE=%s\n' "$PORT_FILE" > "$PROBE_OUT.tmp" && mv "$PROBE_OUT.tmp" "$PROBE_OUT"
if [ -n "$BIND_PORT" ]; then printf '%s' "$BIND_PORT" > "$PORT_FILE"; fi
while [ ! -e "$RELEASE" ]; do sleep 0.02; done`

func startPortStub(t *testing.T, extra ...string) (*Pod, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	release := filepath.Join(dir, "release")
	stub := writeStub(t, dir, portStub)
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
	return pod, id, out, release
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

func TestPodPointsPortFileAtItsRunDirectory(t *testing.T) {
	pod, id, out, _ := startPortStub(t)
	want := filepath.Join(pod.opts.RunsDir, id, PortFileName)
	if seen := envSeen(t, out); seen["PORT_FILE"] != want {
		t.Fatalf("PORT_FILE = %q, want %q: a workload binding port 0 reports its port beside its own run", seen["PORT_FILE"], want)
	}
}

func TestPodKeepsACallerSuppliedPortFile(t *testing.T) {
	_, _, out, _ := startPortStub(t, "PORT_FILE=/elsewhere/port")
	if seen := envSeen(t, out); seen["PORT_FILE"] != "/elsewhere/port" {
		t.Fatalf("PORT_FILE = %q, the runner replaced a caller supplied port file", seen["PORT_FILE"])
	}
}

func TestPodReportsThePortItsProcessBound(t *testing.T) {
	pod, id, _, _ := startPortStub(t, "BIND_PORT=43117")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := pod.Observe(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != runner.StateRunning {
			t.Fatalf("state = %s, the stub is still running", status.State)
		}
		if status.BoundPort == 43117 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Observe never reported the port the process wrote to its port file")
}

func TestPodReportsNoPortUntilTheProcessWritesOne(t *testing.T) {
	pod, id, _, _ := startPortStub(t)
	status, err := pod.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.BoundPort != 0 {
		t.Fatalf("BoundPort = %d before the process wrote any port", status.BoundPort)
	}
}

func TestPodIgnoresAPortFileThatIsNotAPort(t *testing.T) {
	pod, id, _, _ := startPortStub(t, "BIND_PORT=not-a-port")
	time.Sleep(100 * time.Millisecond)
	status, err := pod.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.BoundPort != 0 {
		t.Fatalf("BoundPort = %d from a port file that holds no port", status.BoundPort)
	}
}
