//go:build linux

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
)

const (
	helperEnv  = "PROCGROUP_HELPER"
	helperName = "PROCGROUP_NAME"
	helperLog  = "PROCGROUP_LOG"
	readyLine  = "ready"
)

func TestMain(m *testing.M) {
	if os.Getenv(providerEnv) != "" {
		os.Exit(runProvider())
	}
	if os.Getenv(helperEnv) == "" {
		os.Exit(m.Run())
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	fmt.Fprintln(os.Stdout, readyLine)
	<-ch
	if path := os.Getenv(helperLog); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, os.Getenv(helperName))
			f.Close()
		}
	}
	os.Exit(0)
}

func readyWhenAnnounced(ctx context.Context, p *processrunner.Process) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if announced(p.Output()) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("child never announced readiness; its output tail was %q", p.Output())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func announced(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == readyLine {
			return true
		}
	}
	return false
}

func spec(name, logPath string) processrunner.Spec {
	return processrunner.Spec{
		Argv: []string{os.Args[0], "-test.run=TestMain"},
		Env: append(os.Environ(),
			helperEnv+"=1", helperName+"="+name, helperLog+"="+logPath),
	}
}

func step(t *testing.T, name Component, logPath string, ready func(context.Context, *processrunner.Process) error) Step {
	t.Helper()
	return Step{Name: name, Spec: spec(string(name), logPath), Ready: ready}
}

const standIn Component = "stand-in"

func postgresUp() bool { return true }

func readOrder(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reap log: %v", err)
	}
	return strings.Fields(string(raw))
}

func TestCancellingTheSpawnContextDoesNotKillTheGroup(t *testing.T) {
	logPath := t.TempDir() + "/order"
	ctx, cancel := context.WithCancel(context.Background())
	g, err := Spawn(ctx, []Step{step(t, standIn, logPath, readyWhenAnnounced)}, Options{PostgresUp: postgresUp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cancel()

	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("the group stopped itself when its spawn context was cancelled: %s records a "+
			"stop, so a gateway's SIGTERM SIGKILLs k3s, kine and postgres instead of reaping them "+
			"in reverse order -- and a killed apiserver is a store that needs crash recovery "+
			"before it can be read again", logPath)
	}
	if err := g.Reap(context.Background()); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if got := readOrder(t, logPath); len(got) != 1 || got[0] != string(standIn) {
		t.Fatalf("reap log = %v, want the child to stop only when it is reaped", got)
	}
}

func TestSpawnGatesEachStepOnThePreviousOne(t *testing.T) {
	logPath := t.TempDir() + "/order"
	var gates []Component
	gated := func(c Component) func(context.Context, *processrunner.Process) error {
		return func(ctx context.Context, p *processrunner.Process) error {
			gates = append(gates, c)
			return readyWhenAnnounced(ctx, p)
		}
	}
	g, err := Spawn(context.Background(), []Step{
		step(t, standIn, logPath, gated(standIn)),
		step(t, ComponentOpenBao, logPath, gated(ComponentOpenBao)),
	}, Options{PostgresUp: postgresUp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer g.Reap(context.Background())

	if got := g.Order(); len(got) != 2 || got[0] != standIn || got[1] != ComponentOpenBao {
		t.Fatalf("spawn order = %v", got)
	}
	if len(gates) != 2 {
		t.Fatalf("gates ran %d times, want 2", len(gates))
	}
}

func TestReapIsReverseOrder(t *testing.T) {
	logPath := t.TempDir() + "/order"
	g, err := Spawn(context.Background(), []Step{
		step(t, standIn, logPath, readyWhenAnnounced),
		step(t, ComponentOpenBao, logPath, readyWhenAnnounced),
		step(t, ComponentKine, logPath, readyWhenAnnounced),
		step(t, ComponentK3s, logPath, readyWhenAnnounced),
	}, Options{PostgresUp: postgresUp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := g.Reap(context.Background()); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	got := readOrder(t, logPath)
	want := []string{"k3s-server", "kine", "openbao", "stand-in"}
	if len(got) != len(want) {
		t.Fatalf("reap log = %v, want %v -- every started component must be stopped", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reap order = %v, want %v -- REVERSE order matters: k3s holds the "+
				"store open and kine holds postgres, so stopping the store first leaves "+
				"the components above it writing to a closed socket", got, want)
		}
	}
}

func TestTheCloudControllerManagerIsBroughtUpAfterK3sAndReapedFirst(t *testing.T) {
	at := map[Component]int{}
	for i, c := range StackOrder {
		at[c] = i
	}
	for _, lower := range []Component{ComponentK3s, ComponentKine, ComponentOpenBao, ComponentPostgres} {
		if at[ComponentCloudControllerManager] <= at[lower] {
			t.Fatalf("%s is not after %s in StackOrder (%v): the controller is useless without an "+
				"apiserver, and a step placed before k3s would move the identity guard's seam",
				ComponentCloudControllerManager, lower, StackOrder)
		}
	}
	def := map[Component]int{}
	for i, c := range DefaultOrder {
		def[c] = i
	}
	if def[ComponentCloudControllerManager] <= def[ComponentK3s] {
		t.Fatalf("DefaultOrder puts the controller at or before k3s (%v)", DefaultOrder)
	}

	logPath := t.TempDir() + "/order"
	g, err := Spawn(context.Background(), []Step{
		step(t, standIn, logPath, readyWhenAnnounced),
		step(t, ComponentKine, logPath, readyWhenAnnounced),
		step(t, ComponentK3s, logPath, readyWhenAnnounced),
		step(t, ComponentCloudControllerManager, logPath, readyWhenAnnounced),
	}, Options{PostgresUp: postgresUp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := g.Reap(context.Background()); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	got := readOrder(t, logPath)
	want := []string{"socialweb-cloud-controller-manager", "k3s-server", "kine", "stand-in"}
	if len(got) != len(want) {
		t.Fatalf("reap log = %v, want %v -- every started component must be stopped", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reap order = %v, want %v. The controller is stopped FIRST: a controller that "+
				"outlives the apiserver it reconciles against loops on connection errors during every "+
				"teardown", got, want)
		}
	}
}

func TestFailedStepReapsEverythingBeforeIt(t *testing.T) {
	logPath := t.TempDir() + "/order"
	boom := errors.New("kine refused to start")
	_, err := Spawn(context.Background(), []Step{
		step(t, standIn, logPath, readyWhenAnnounced),
		step(t, ComponentOpenBao, logPath, readyWhenAnnounced),
		step(t, ComponentKine, logPath, func(context.Context, *processrunner.Process) error {
			return boom
		}),
	}, Options{PostgresUp: postgresUp})
	if err == nil {
		t.Fatal("Spawn returned nil for a step whose gate failed")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("the gate's error was swallowed: %v", err)
	}
	got := readOrder(t, logPath)
	if len(got) != 2 || got[0] != "openbao" || got[1] != "stand-in" {
		t.Fatalf("reap log = %v, want the two started components in reverse -- A PARTIALLY "+
			"STARTED GROUP LEFT RUNNING IS A LEAK WITH A BOUND PORT AND A LIVE APISERVER", got)
	}
}

func TestOpenBaoRefusesToStartWithoutItsOwnPostgres(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
	}{
		{"the predicate was never wired", Options{}},
		{"the predicate says no", Options{PostgresUp: func() bool { return false }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := t.TempDir() + "/order"
			_, err := Spawn(context.Background(), []Step{
				step(t, ComponentOpenBao, logPath, readyWhenAnnounced),
			}, tc.opts)
			if err == nil {
				t.Fatal("openbao started without its own postgres -- every cluster's DSN is " +
					"byte-identical, so it would reach ANOTHER TENANT'S and authenticate")
			}
			if !errors.Is(err, ErrPostgresUnavailable) {
				t.Fatalf("want ErrPostgresUnavailable, got %v", err)
			}
			if !strings.Contains(err.Error(), "postgres") || !strings.Contains(err.Error(), "store") {
				t.Fatalf("the refusal does not name the store the vault needs: %v", err)
			}
			if _, statErr := os.Stat(logPath); statErr == nil {
				t.Fatal("openbao was actually started before the refusal, and had to be reaped")
			}
		})
	}
}

func TestPostgresCannotBeAStep(t *testing.T) {
	_, err := Spawn(context.Background(), []Step{
		{Name: ComponentPostgres, Spec: processrunner.Spec{}, Ready: func(context.Context, *processrunner.Process) error { return nil }},
		step(t, ComponentOpenBao, t.TempDir()+"/order", readyWhenAnnounced),
	}, Options{PostgresUp: postgresUp})
	if err == nil {
		t.Fatal("a postgres Step with no argv was accepted; embedded-postgres is a Go " +
			"library and there is no binary for this group to run")
	}
	if !strings.Contains(err.Error(), "empty argv") {
		t.Fatalf("a postgres step failed for some other reason than having no argv: %v -- "+
			"if this now fails with ErrPostgresUnavailable the guard moved back to the "+
			"process table and became unsatisfiable again", err)
	}
}

func TestStepWithoutAReadinessGateIsRefusedAndReaped(t *testing.T) {
	_, err := Spawn(context.Background(), []Step{
		{Name: standIn, Spec: spec("pg", t.TempDir()+"/unused")},
	}, Options{})
	if err == nil {
		t.Fatal("a step with no readiness gate was accepted -- the next component would " +
			"then start against a port nothing is listening on")
	}
	if !strings.Contains(err.Error(), "no readiness gate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEveryComponentTheDefaultSetNamesIsWalked(t *testing.T) {
	at := make(map[Component]int, len(StackOrder))
	for i, c := range StackOrder {
		at[c] = i
	}
	last := -1
	for _, c := range DefaultOrder {
		i, ok := at[c]
		if !ok {
			t.Fatalf("procgroup.DefaultOrder names %s and procgroup.StackOrder does not. A "+
				"component the default set brings up, that the walk a caller uses cannot see, is "+
				"one an explicitly opted-in stack SILENTLY DROPS: the stack is accepted, the port "+
				"is leased, the config is rendered and no step is built -- so the lease is held by "+
				"nothing and the group reaches Ready over a component that never started. "+
				"DefaultOrder is the default SET; StackOrder is every component a stack may name", c)
		}
		if i < last {
			t.Fatalf("procgroup.DefaultOrder and procgroup.StackOrder disagree about bring-up "+
				"order: this walk reached %s out of order (%v vs %v). Reap is reverse order, and "+
				"k3s holds the store open while kine holds postgres, so a group that comes up in "+
				"one order and reaps in another stops the store over a component still writing to it",
				c, DefaultOrder, StackOrder)
		}
		last = i
	}
}

func TestReapIsIdempotent(t *testing.T) {
	logPath := t.TempDir() + "/order"
	g, err := Spawn(context.Background(), []Step{
		step(t, standIn, logPath, readyWhenAnnounced),
	}, Options{PostgresUp: postgresUp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := g.Reap(context.Background()); err != nil {
		t.Fatalf("first Reap: %v", err)
	}
	if err := g.Reap(context.Background()); err != nil {
		t.Fatalf("second Reap: %v", err)
	}
	if got := readOrder(t, logPath); len(got) != 1 {
		t.Fatalf("the child was signalled %d times (%v); Reap must be idempotent", len(got), got)
	}
}

func TestGracePeriodKillsAChildThatIgnoresTheSignal(t *testing.T) {
	g, err := Spawn(context.Background(), []Step{
		{Name: standIn, Spec: processrunner.Spec{Argv: []string{"/bin/sleep", "60"}}, Ready: func(context.Context, *processrunner.Process) error { return nil }},
	}, Options{GracePeriod: 200 * time.Millisecond, PostgresUp: postgresUp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	p, ok := g.Process(standIn)
	if !ok {
		t.Fatal("the stand-in step is not in the group")
	}
	pid := p.PID

	done := make(chan error, 1)
	go func() { done <- g.Reap(context.Background()) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Reap did not return -- a child that ignores SIGTERM must be KILLED after " +
			"the grace period, not waited on forever")
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("pid %d survived the grace period", pid)
	}
}
