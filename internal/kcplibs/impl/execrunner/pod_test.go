//go:build linux

package execrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

func envSeen(t *testing.T, out string) map[string]string {
	t.Helper()
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			seen[key] = value
		}
	}
	return seen
}

const envStub = `printf 'DENO_DIR=%s\nDENO_CERT=%s\nKCP_NAMESPACE=%s\n' "$DENO_DIR" "$DENO_CERT" "$KCP_NAMESPACE" > "$PROBE_OUT.tmp" && mv "$PROBE_OUT.tmp" "$PROBE_OUT"`

func TestPodLeavesTheCallersNamespaceAlone(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	stub := writeStub(t, dir, envStub)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs"), ExtraEnv: []string{"PROBE_OUT=" + out}})
	if err != nil {
		t.Fatal(err)
	}
	request := runner.PodRequest{Name: "pds", Script: "x", Env: map[string]string{"KCP_NAMESPACE": "team-a"}}
	id, err := pod.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if seen := envSeen(t, out); seen["KCP_NAMESPACE"] != "team-a" {
		t.Fatalf("KCP_NAMESPACE = %q, the runner has no namespace of its own and must not replace the one the caller set", seen["KCP_NAMESPACE"])
	}
}

func TestPodKeepsACallerSuppliedDenoDir(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	stub := writeStub(t, dir, envStub)
	pod, err := NewPod(PodOptions{
		DenoBin:  stub,
		RunsDir:  filepath.Join(dir, "runs"),
		ExtraEnv: []string{"DENO_DIR=/sdks/deno", "PROBE_OUT=" + out},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if seen := envSeen(t, out); seen["DENO_DIR"] != "/sdks/deno" {
		t.Fatalf("DENO_DIR = %q, the runner overwrote a caller supplied module cache", seen["DENO_DIR"])
	}
}

func TestPodDefaultsDenoDirToTheRunDirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	runsDir := filepath.Join(dir, "runs")
	stub := writeStub(t, dir, envStub)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: runsDir, ExtraEnv: []string{"PROBE_OUT=" + out}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if seen := envSeen(t, out); seen["DENO_DIR"] != filepath.Join(runsDir, id, ".deno") {
		t.Fatalf("DENO_DIR = %q, want the run's own module cache", seen["DENO_DIR"])
	}
}

func TestPodNamespaceDirIsTheModuleCache(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	stub := writeStub(t, dir, envStub)
	pod, err := NewPod(PodOptions{
		DenoBin:      stub,
		RunsDir:      filepath.Join(dir, "runs"),
		NamespaceDir: "/shared/deno",
		ExtraEnv:     []string{"PROBE_OUT=" + out},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if seen := envSeen(t, out); seen["DENO_DIR"] != "/shared/deno" {
		t.Fatalf("DENO_DIR = %q, want the namespace directory the caller configured", seen["DENO_DIR"])
	}
}

func TestPodKeepsACallerSuppliedDenoCERT(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	runsDir := filepath.Join(dir, "runs")
	stub := writeStub(t, dir, envStub)
	pod, err := NewPod(PodOptions{
		DenoBin:  stub,
		RunsDir:  runsDir,
		CAData:   []byte("a root"),
		ExtraEnv: []string{"DENO_CERT=/certs/ca.pem", "PROBE_OUT=" + out},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if seen := envSeen(t, out); seen["DENO_CERT"] != "/certs/ca.pem" {
		t.Fatalf("DENO_CERT = %q, the runner overwrote a caller supplied trust bundle", seen["DENO_CERT"])
	}
}

func TestPodPointsDenoCERTAtItsOwnRunDirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	runsDir := filepath.Join(dir, "runs")
	stub := writeStub(t, dir, envStub)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: runsDir, CAData: []byte("a root"), ExtraEnv: []string{"PROBE_OUT=" + out}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
	if err != nil {
		t.Fatal(err)
	}
	observeUntilDone(t, pod, id)
	if seen := envSeen(t, out); seen["DENO_CERT"] != filepath.Join(runsDir, id, "ca.pem") {
		t.Fatalf("DENO_CERT = %q, want the bundle written beside the run", seen["DENO_CERT"])
	}
}

func TestEngineDefaultsDenoDirToItsRunDirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	runsDir := filepath.Join(dir, "runs")
	stub := writeStub(t, dir, envStub)
	engine, err := NewEngine(EngineOptions{
		DenoBin:    stub,
		ServerDir:  dir,
		ServerFile: filepath.Join(dir, "server.ts"),
		RunsDir:    runsDir,
		ExtraEnv:   []string{"PROBE_OUT=" + out},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := engine.Start(context.Background(), runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if seen := envSeen(t, out); seen["DENO_DIR"] != filepath.Join(runsDir, id, ".deno") {
		t.Fatalf("DENO_DIR = %q, want the run's own module cache", seen["DENO_DIR"])
	}
	_ = engine.Stop(context.Background(), id)
}

func TestEngineKeepsACallerSuppliedDenoDir(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	stub := writeStub(t, dir, envStub)
	engine, err := NewEngine(EngineOptions{
		DenoBin:    stub,
		ServerDir:  dir,
		ServerFile: filepath.Join(dir, "server.ts"),
		RunsDir:    filepath.Join(dir, "runs"),
		ExtraEnv:   []string{"DENO_DIR=/sdks/deno", "PROBE_OUT=" + out},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := engine.Start(context.Background(), runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if seen := envSeen(t, out); seen["DENO_DIR"] != "/sdks/deno" {
		t.Fatalf("DENO_DIR = %q, the runner overwrote a caller supplied module cache", seen["DENO_DIR"])
	}
	_ = engine.Stop(context.Background(), id)
}

func writeStub(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func observeUntilDone(t *testing.T, pod *Pod, id string) runner.PodStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := pod.Observe(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != runner.StateRunning {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the stub never finished")
	return runner.PodStatus{}
}

func TestPodWritesOutputsAndExitCode(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `printf '{"answer":"42"}' > result.json`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	status := observeUntilDone(t, pod, id)
	if status.State != runner.StateSucceeded || status.ExitCode != 0 {
		t.Fatalf("status = %+v", status)
	}
	if status.Outputs["answer"] != "42" {
		t.Fatalf("outputs = %v", status.Outputs)
	}
}

func TestPodReportsANonZeroExit(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `exit 3`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	status := observeUntilDone(t, pod, id)
	if status.State != runner.StateFailed || status.ExitCode != 3 {
		t.Fatalf("status = %+v", status)
	}
	if status.Message == "" {
		t.Fatal("a failure must carry a message")
	}
}

func TestPodStopIsReportedAsFailure(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `sleep 30`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pod.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	status := observeUntilDone(t, pod, id)
	if status.State != runner.StateFailed || status.Message != "the deno process was stopped" {
		t.Fatalf("status = %+v", status)
	}
}

func TestARecoveredRunTheRecordCallsStoppedHasFinished(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	id := "pod-stopped"
	runDir := filepath.Join(runsDir, id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"pid":%d,"started":%q,"ticks":1,"stopped":true}`, os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	pod, err := NewPod(PodOptions{DenoBin: "true", RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err := pod.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateFailed || status.Message != "the deno process was stopped" {
		t.Fatalf("a run the record calls stopped must not read as running: %+v", status)
	}
}

func TestARecoveredRunThatLeftItsMarkerHasFinished(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	id := "pod-recycled"
	runDir := filepath.Join(runsDir, id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "done.json"), []byte(`{"exitCode":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "result.json"), []byte(`{"answer":"42"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"pid":%d,"started":%q,"ticks":1}`, os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	pod, err := NewPod(PodOptions{DenoBin: "true", RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err := pod.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateSucceeded {
		t.Fatalf("a run that wrote its exit marker has finished, whatever the pid belongs to now: %+v", status)
	}
}

func TestPodRecoversARunFromItsDirectory(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	stub := writeStub(t, dir, `printf '{"answer":"42"}' > result.json`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	if status := observeUntilDone(t, pod, id); status.State != runner.StateSucceeded {
		t.Fatalf("status = %+v", status)
	}

	restarted, err := NewPod(PodOptions{DenoBin: stub, RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err := restarted.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateSucceeded || status.ExitCode != 0 || status.Outputs["answer"] != "42" {
		t.Fatalf("recovered status = %+v", status)
	}
}

func TestPodProbeRunsInTheRunDirectory(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `exit 0`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	passed, err := pod.Probe(context.Background(), id, []string{"/bin/sh", "-c", "test -f main.ts"}, time.Second)
	if err != nil || !passed {
		t.Fatalf("probe = (%v, %v)", passed, err)
	}
	passed, err = pod.Probe(context.Background(), id, []string{"/bin/sh", "-c", "test -f absent"}, time.Second)
	if err != nil || passed {
		t.Fatalf("failing probe = (%v, %v)", passed, err)
	}
	if passed, err := pod.Probe(context.Background(), id, nil, time.Second); err != nil || !passed {
		t.Fatalf("an empty probe passes = (%v, %v)", passed, err)
	}
	observeUntilDone(t, pod, id)
}

func TestPodWritesTheConfigFilesDenoReads(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `exit 0`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{
		Name:     "pds",
		DenoJSON: `{"imports":{"x":"./x.ts"}}`,
		DenoLock: `{"version":"5"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := []string{"/bin/sh", "-c", `grep -q '"x"' deno.json && grep -q '"version"' deno.lock`}
	passed, err := pod.Probe(context.Background(), id, probe, time.Second)
	if err != nil || !passed {
		t.Fatalf("deno auto-discovers a config only under its own names, probe = (%v, %v)", passed, err)
	}
	observeUntilDone(t, pod, id)
}

func TestPodRunnerTimeoutFailsTheRun(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `sleep 30`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs"), Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	status := observeUntilDone(t, pod, id)
	if status.State != runner.StateFailed || status.Message != "the deno process exceeded the runner timeout" {
		t.Fatalf("status = %+v", status)
	}
}

func TestEngineStartAndObserve(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `sleep 30`)
	engine, err := NewEngine(EngineOptions{DenoBin: stub, ServerDir: dir, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := engine.Start(context.Background(), runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	status, err := engine.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateRunning {
		t.Fatalf("status = %+v", status)
	}
	if err := engine.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	status, err = engine.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateFailed {
		t.Fatalf("status = %+v", status)
	}
}

func TestNewPodRequiresARunsDirectory(t *testing.T) {
	if _, err := NewPod(PodOptions{}); err == nil {
		t.Fatal("RunsDir is required")
	}
	if _, err := NewEngine(EngineOptions{RunsDir: t.TempDir()}); err == nil {
		t.Fatal("ServerDir is required")
	}
}

func TestPodTrustsAResultOverAMissingDoneMarker(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	id := "pod-orphan"
	runDir := filepath.Join(runsDir, id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "result.json"), []byte(`{"answer":"42"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"pid":%d,"started":"2020-01-01T00:00:00Z","ticks":1}`, gone.Process.Pid)
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	pod, err := NewPod(PodOptions{DenoBin: "true", RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err := pod.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateSucceeded {
		t.Fatalf("a run that wrote its result finished its work, whatever became of the marker: %+v", status)
	}
	if status.Outputs["answer"] != "42" {
		t.Fatalf("the outputs it did write must survive: %v", status.Outputs)
	}
	if err := os.Remove(filepath.Join(runDir, "result.json")); err != nil {
		t.Fatal(err)
	}
	orphan, err := NewPod(PodOptions{DenoBin: "true", RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err = orphan.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateFailed {
		t.Fatalf("with no result and no marker there is nothing to call a success: %+v", status)
	}
}

func TestPodForgetsAFinishedRunAndStillAnswers(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `printf '{"answer":"42"}' > result.json`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	if status := observeUntilDone(t, pod, id); status.State != runner.StateSucceeded {
		t.Fatalf("status = %+v", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && pod.sup.held() != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if pod.sup.held() != 0 {
		t.Fatalf("the supervisor still holds %d finished runs", pod.sup.held())
	}
	status, err := pod.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateSucceeded || status.Outputs["answer"] != "42" {
		t.Fatalf("a forgotten run must still answer from its directory: %+v", status)
	}
}

func TestPodDoesNotKillARecycledPID(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	stub := writeStub(t, dir, `sleep 30`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, id, "state.json"), []byte(`{"pid":1,"started":"2020-01-01T00:00:00Z","ticks":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewPod(PodOptions{DenoBin: stub, RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if !processAlive(1) {
		t.Fatal("a recovered run whose pid no longer matches must not be killed")
	}
	_ = pod.Stop(context.Background(), id)
}

func TestEngineWithoutADeadlineIsNeverReaped(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `sleep 30`)
	engine, err := NewEngine(EngineOptions{
		DenoBin:    stub,
		ServerDir:  dir,
		ServerFile: filepath.Join(dir, "server.ts"),
		RunsDir:    filepath.Join(dir, "runs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := engine.Start(context.Background(), runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	defer func(restore func() time.Time) { engineNow = restore }(engineNow)
	engineNow = func() time.Time { return time.Now().Add(1000 * time.Hour) }
	status, err := engine.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateRunning {
		t.Fatalf("an engine with no deadline set must run until something else stops it: %+v", status)
	}
	_ = engine.Stop(context.Background(), id)
}

func TestEngineEnforcesItsTimeout(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `sleep 30`)
	engine, err := NewEngine(EngineOptions{DenoBin: stub, ServerDir: dir, RunsDir: filepath.Join(dir, "runs"), Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	id, err := engine.Start(context.Background(), runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := engine.Observe(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == runner.StateFailed {
			if status.Message != "the policy engine exceeded the runner timeout" {
				t.Fatalf("message = %q", status.Message)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the engine never hit its timeout")
}

func TestAFailedStartLeavesNoRunDirectory(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	pod, err := NewPod(PodOptions{DenoBin: filepath.Join(dir, "absent"), RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"}); err == nil {
		t.Fatal("a binary that is not there must fail the start")
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed start left %d directories behind", len(entries))
	}
}

func TestRunIdentifiersDoNotRepeatWithinASecond(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `exit 0`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	ids := make([]string, 0, 5)
	for range 5 {
		id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("%s was handed out twice, and a second run would overwrite the first", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, id := range ids {
		observeUntilDone(t, pod, id)
	}
}

func TestARecoveredRunWithUnknownTicksIsStopped(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL) })

	id := "pod-unknown-ticks"
	runDir := filepath.Join(runsDir, id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"pid":%d,"started":%q}`, child.Process.Pid, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	pod, err := NewPod(PodOptions{DenoBin: "true", RunsDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := pod.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a recovered run whose ticks are unknown must still be stopped")
	}
}

func TestPodProbeAfterExitKeepsTheRunEnvironment(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "probe-env.txt")
	stub := writeStub(t, dir, `exit 0`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := pod.Start(context.Background(), runner.PodRequest{
		Name:   "pds",
		Script: "x",
		Env:    map[string]string{"KCP_DNS_TABLE": "team-a.svc", "PROBE_OUT": out},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status := observeUntilDone(t, pod, id); status.State != runner.StateSucceeded {
		t.Fatalf("status = %+v", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && pod.sup.held() != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	probe := []string{"/bin/sh", "-c", `printf '%s' "$KCP_DNS_TABLE" > "$PROBE_OUT"`}
	passed, err := pod.Probe(context.Background(), id, probe, time.Second)
	if err != nil || !passed {
		t.Fatalf("probe = (%v, %v)", passed, err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "team-a.svc" {
		t.Fatalf("the probe's environment lost the run's variables: %q", body)
	}
}

func TestTheSupervisorBoundsRetainedRuns(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `if [ -n "$HOLD" ]; then sleep 30; else exit 0; fi`)
	pod, err := NewPod(PodOptions{DenoBin: stub, RunsDir: filepath.Join(dir, "runs")})
	if err != nil {
		t.Fatal(err)
	}
	pod.sup.retainLimit = 4
	finished := make([]string, 0, 7)
	for range 7 {
		id, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x"})
		if err != nil {
			t.Fatal(err)
		}
		finished = append(finished, id)
	}
	for _, id := range finished {
		observeUntilDone(t, pod, id)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && pod.sup.retained() > pod.sup.retainLimit {
		time.Sleep(5 * time.Millisecond)
	}
	if got := pod.sup.retained(); got > pod.sup.retainLimit {
		t.Fatalf("the supervisor retains %d finished run environments, but the bound is %d", got, pod.sup.retainLimit)
	}

	live, err := pod.Start(context.Background(), runner.PodRequest{Name: "pds", Script: "x", Env: map[string]string{"HOLD": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pod.sup.lookup(live); !ok {
		t.Fatal("a run that is still alive must not be evicted")
	}
	if env := pod.sup.envOf(live); env["HOLD"] != "1" {
		t.Fatalf("a live run's environment must be retained: %v", env)
	}
	if err := pod.Stop(context.Background(), live); err != nil {
		t.Fatal(err)
	}
}

func TestEngineReportsTheWaitErrorWhenStopped(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, `sleep 30`)
	engine, err := NewEngine(EngineOptions{
		DenoBin:    stub,
		ServerDir:  dir,
		ServerFile: filepath.Join(dir, "server.ts"),
		RunsDir:    filepath.Join(dir, "runs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := engine.Start(context.Background(), runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Observe(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateFailed {
		t.Fatalf("status = %+v", status)
	}
	if status.Message == "" || status.Message == "the policy engine process was stopped" {
		t.Fatalf("message = %q, want the wait error", status.Message)
	}
}
