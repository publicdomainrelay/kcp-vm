package memoryrunner

import (
	"context"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/abc/runner"
)

func TestPodRunsUntilDone(t *testing.T) {
	pod := NewPod(PodOptions{
		Outcome:         runner.PodStatus{State: runner.StateSucceeded, ExitCode: 0},
		PollsBeforeDone: 2,
	})
	ctx := context.Background()
	id, err := pod.Start(ctx, runner.PodRequest{Name: "pds", Script: "console.log(1)"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := pod.Observe(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != runner.StateRunning {
		t.Fatalf("first observe = %v", first.State)
	}
	second, err := pod.Observe(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != runner.StateSucceeded {
		t.Fatalf("second observe = %v", second.State)
	}
	if _, err := pod.Observe(ctx, "missing"); err == nil {
		t.Fatal("an unknown run must be an error")
	}
}

func TestPodStopMarksFailure(t *testing.T) {
	pod := NewPod(PodOptions{PollsBeforeDone: 100})
	ctx := context.Background()
	id, err := pod.Start(ctx, runner.PodRequest{Name: "pds"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pod.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	status, err := pod.Observe(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateFailed {
		t.Fatalf("status = %+v", status)
	}
}

func TestPodProbe(t *testing.T) {
	pod := NewPod(PodOptions{ProbeResult: true})
	passed, err := pod.Probe(context.Background(), "any", []string{"true"}, time.Second)
	if err != nil || !passed {
		t.Fatalf("probe = (%v, %v)", passed, err)
	}
}

func TestEngineExitsAfterPolls(t *testing.T) {
	engine := NewEngine(EngineOptions{ExitsAfterPolls: 2})
	ctx := context.Background()
	id, err := engine.Start(ctx, runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := engine.Observe(ctx, id); status.State != runner.StateRunning {
		t.Fatalf("first observe = %+v", status)
	}
	if status, _ := engine.Observe(ctx, id); status.State != runner.StateFailed {
		t.Fatalf("second observe = %+v", status)
	}
}

func TestEngineStop(t *testing.T) {
	engine := NewEngine(EngineOptions{})
	ctx := context.Background()
	id, err := engine.Start(ctx, runner.EngineRequest{Name: "gha-lite", Port: 8787})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Observe(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != runner.StateFailed || status.Message != "stopped" {
		t.Fatalf("status = %+v", status)
	}
}
