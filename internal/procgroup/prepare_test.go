//go:build linux

package procgroup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
)

func TestPrepareRunsAfterTheStepsBeforeItAreReadyAndItsSpecIsWhatStarts(t *testing.T) {
	logPath := t.TempDir() + "/order"
	var k3sReady bool
	k3s := step(t, ComponentK3s, logPath, func(ctx context.Context, p *processrunner.Process) error {
		if err := readyWhenAnnounced(ctx, p); err != nil {
			return err
		}
		k3sReady = true
		return nil
	})
	link := step(t, ComponentApiserverLink, logPath, readyWhenAnnounced)
	var sawK3sReady bool
	link.Prepare = func(_ context.Context, s processrunner.Spec) (processrunner.Spec, error) {
		sawK3sReady = k3sReady
		s.Env = append(append([]string{}, s.Env...), helperName+"=prepared-link")
		return s, nil
	}
	g, err := Spawn(context.Background(), []Step{k3s, link}, Options{PostgresUp: postgresUp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !sawK3sReady {
		t.Fatal("the link was prepared before k3s was ready: a link's key lives in a vault that " +
			"is only up once the steps before it have passed their gates")
	}
	if err := g.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := readOrder(t, logPath)
	if strings.Join(got, " ") != "prepared-link k3s-server" {
		t.Fatalf("reap log = %v, want the PREPARED link stopped first and then k3s", got)
	}
}

func TestAFailedPrepareStartsNothingAndReapsWhatCameBefore(t *testing.T) {
	logPath := t.TempDir() + "/order"
	boom := errors.New("the vault would not hand over the link key")
	link := step(t, ComponentOpenBaoLink, logPath, readyWhenAnnounced)
	link.Prepare = func(context.Context, processrunner.Spec) (processrunner.Spec, error) {
		return processrunner.Spec{}, boom
	}
	_, err := Spawn(context.Background(), []Step{step(t, ComponentK3s, logPath, readyWhenAnnounced), link},
		Options{PostgresUp: postgresUp})
	if !errors.Is(err, boom) {
		t.Fatalf("Spawn = %v, want the prepare error", err)
	}
	if got := readOrder(t, logPath); strings.Join(got, " ") != "k3s-server" {
		t.Fatalf("reap log = %v, want only k3s, which had started", got)
	}
}
