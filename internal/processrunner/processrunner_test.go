//go:build linux

package processrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	helperEnv  = "PROCESSRUNNER_HELPER"
	floodEnv   = "PROCESSRUNNER_FLOOD_BYTES"
	orphanEnv  = "PROCESSRUNNER_ORPHAN_PIDFILE"
	floodStart = "TAIL-START\n"
	floodEnd   = "TAIL-END\n"
	floodErr   = "FLOOD-STDERR\n"
	floodBytes = 500000
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		switch mode {
		case "exit7":
			fmt.Fprintln(os.Stdout, "child-stdout")
			os.Exit(7)
		case "hold":
			time.Sleep(60 * time.Second)
			os.Exit(0)
		case "flood":
			n := floodBytes
			if raw := os.Getenv(floodEnv); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil {
					os.Exit(2)
				}
				n = parsed
			}
			flood(n)
			os.Exit(0)
		case "orphan":
			name := os.Getenv(orphanEnv)
			cmd := exec.Command("sleep", "30")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Start(); err != nil {
				os.Exit(4)
			}
			if err := os.WriteFile(name, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
				os.Exit(5)
			}
			os.Exit(0)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func flood(n int) {
	_, _ = os.Stdout.WriteString(floodStart)
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for written := 0; written < n; {
		emit := chunk
		if remaining := n - written; remaining < len(emit) {
			emit = emit[:remaining]
		}
		if _, err := os.Stdout.Write(emit); err != nil {
			os.Exit(3)
		}
		written += len(emit)
	}
	_, _ = os.Stdout.WriteString(floodEnd)
	_, _ = os.Stderr.WriteString(floodErr)
}

func floodHelper(t *testing.T, n int) Spec {
	t.Helper()
	spec := helper(t, "flood")
	spec.Env = append(spec.Env, floodEnv+"="+strconv.Itoa(n))
	return spec
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func helper(t *testing.T, mode string) Spec {
	t.Helper()
	return Spec{
		Argv: []string{os.Args[0], "-test.run=TestMain"},
		Env:  append(os.Environ(), helperEnv+"="+mode),
	}
}

func TestSpawnAndReap(t *testing.T) {
	p, err := Start(context.Background(), helper(t, "exit7"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := p.PID
	if pid <= 0 {
		t.Fatalf("PID not assigned: %d", pid)
	}
	if err := p.Wait(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 7 {
			t.Fatalf("Wait: want exit code 7, got %v", err)
		}
	} else {
		t.Fatal("Wait returned nil for a child that exited 7")
	}
	if alive(pid) {
		t.Fatalf("pid %d is still alive after Wait -- the child was NOT reaped", pid)
	}
}

func TestPrefixIsPrepended(t *testing.T) {
	spec := helper(t, "exit7")
	spec.Prefix = []string{"/usr/bin/env"}
	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start with prefix: %v", err)
	}
	_ = p.Wait()
	if out := p.Output(); !strings.Contains(out, "child-stdout") {
		t.Fatalf("prefix was not prepended before argv: child stdout = %q", out)
	}
}

func TestTailKeepsTheLastBytesAndEvictsTheOldest(t *testing.T) {
	p, err := Start(context.Background(), floodHelper(t, floodBytes))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	out := p.Output()
	if len(out) != OutputTailBytes {
		t.Fatalf("tail is %d bytes, want exactly %d. A ring that grows with the child's "+
			"output is unbounded memory in the gateway -- the child is k3s, which logs for "+
			"the life of the cluster", len(out), OutputTailBytes)
	}
	if !strings.Contains(out, floodEnd) {
		t.Fatalf("the most recent output is not in the tail: %q is missing from the last "+
			"%d bytes", strings.TrimSpace(floodEnd), OutputTailBytes)
	}
	if strings.Contains(out, floodStart) {
		t.Fatalf("the OLDEST output survived: %q is still in the tail, so the tail is not "+
			"a window over the most recent %d bytes", strings.TrimSpace(floodStart), OutputTailBytes)
	}
	if !strings.Contains(out, floodErr) {
		t.Fatalf("stderr did not reach the tail: %q is missing. An agent that reports its "+
			"failure on stderr would be invisible", strings.TrimSpace(floodErr))
	}
}

func TestTailStaysBoundedWhenTheChildFloodsFarPastIt(t *testing.T) {
	p, err := Start(context.Background(), floodHelper(t, 32*OutputTailBytes))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	out := p.Output()
	if len(out) != OutputTailBytes {
		t.Fatalf("after %d bytes of child output the tail holds %d bytes, want exactly %d -- "+
			"the bound must hold no matter how much the child writes", 32*OutputTailBytes,
			len(out), OutputTailBytes)
	}
	if !strings.Contains(out, floodEnd) {
		t.Fatalf("after %d bytes the newest output is missing from the tail", 32*OutputTailBytes)
	}
}

func TestCallerSuppliedWriterReceivesTheOutput(t *testing.T) {
	var log bytes.Buffer
	spec := floodHelper(t, floodBytes)
	spec.Output = &log
	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(log.String(), floodStart) {
		t.Fatalf("the caller's writer missed the FIRST bytes of the child's output, so it is "+
			"not a log: %d bytes received", log.Len())
	}
	if !strings.Contains(log.String(), floodEnd) {
		t.Fatalf("the caller's writer missed the LAST bytes of the child's output -- a log "+
			"that stops short of the end hides the failure that ended the child: %d bytes received",
			log.Len())
	}
	if !strings.Contains(log.String(), floodErr) {
		t.Fatalf("the caller's writer missed stderr: %d bytes received", log.Len())
	}
	if got := len(p.Output()); got != OutputTailBytes {
		t.Fatalf("a caller-supplied writer changed the tail: got %d bytes, want %d. The two "+
			"sinks are independent; a caller redirecting a component's log must not also "+
			"decide how much the gateway remembers", got, OutputTailBytes)
	}
}

func TestNilWriterLeavesOnlyTheTail(t *testing.T) {
	p, err := Start(context.Background(), floodHelper(t, floodBytes))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out := p.Output(); len(out) != OutputTailBytes {
		t.Fatalf("with no caller writer the tail holds %d bytes, want %d", len(out), OutputTailBytes)
	}
}

func TestTailIsReadableWhileTheChildIsStillWriting(t *testing.T) {
	p, err := Start(context.Background(), floodHelper(t, 16*OutputTailBytes))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	stop := make(chan struct{})
	readsDone := make(chan struct{})
	go func() {
		defer close(readsDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = p.Output()
		}
	}()
	err = p.Wait()
	close(stop)
	<-readsDone
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(p.Output(), floodEnd) {
		t.Fatal("the tail lost the child's final output while a reader was running")
	}
}

func TestStopKillsAHoldingChild(t *testing.T) {
	p, err := Start(context.Background(), helper(t, "hold"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := p.PID
	if !alive(pid) {
		t.Fatalf("pid %d should be alive immediately after Start", pid)
	}
	if err := p.Stop(syscall.SIGKILL); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if alive(pid) {
		t.Fatalf("pid %d survived Stop -- a cluster whose children are never reaped leaks "+
			"for the life of the gateway", pid)
	}
}

func TestStopSucceedsWhenTheChildAlreadyExitedNonZero(t *testing.T) {
	p, err := Start(context.Background(), helper(t, "exit7"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err == nil {
		t.Fatal("Wait: want the helper's non-zero exit status, got nil")
	}
	if err := p.Stop(syscall.SIGINT); err != nil {
		t.Fatalf("Stop: %v -- a child that already exited, even non-zero, is a process that is "+
			"gone, and stopping a node whose guest has exited is a SUCCESS; only a process that "+
			"refuses to die is a failure worth returning. Under the container test rig the qemu "+
			"guest was reaped before pasta was signalled, so Signal returned os.ErrProcessDone and "+
			"Stop returned the child's raw exit status", err)
	}
}

func TestWaitReturnsWhenADescendantHoldsThePipes(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "orphan.pid")
	spec := helper(t, "orphan")
	spec.Env = append(spec.Env, orphanEnv+"="+pidfile)
	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Wait blocked after the direct child exited, because a descendant inherited " +
			"the child's stdout and stderr pipes and still holds them open. The node's guest is " +
			"such a descendant of pasta, so a teardown that signals only pasta blocks here until " +
			"the guest dies on its own -- for ever if it does not")
	}
	if raw, err := os.ReadFile(pidfile); err == nil {
		if pid, conv := strconv.Atoi(strings.TrimSpace(string(raw))); conv == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func TestContextCancelReapsTheChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, helper(t, "hold"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := p.PID
	cancel()
	done := make(chan struct{})
	go func() { _ = p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Wait did not return within 10s of context cancellation")
	}
	if alive(pid) {
		t.Fatalf("pid %d outlived its context", pid)
	}
}

func TestStartDoesNotBlockOnAFloodingChild(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		p, err := Start(context.Background(), floodHelper(t, floodBytes))
		if err != nil {
			done <- fmt.Errorf("Start: %w", err)
			return
		}
		done <- p.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("a child that wrote %d bytes never finished. The child's stdout and stderr "+
			"were handed to nobody, so once the kernel pipe buffer filled the child blocked "+
			"inside write(2) and never reached its exit. k3s server and k3s agent both emit "+
			"far more than one buffer of startup log, so this is the shape the whole cluster "+
			"hangs in -- and it reports as 'the apiserver never became ready', which is a "+
			"readiness fault the log cannot explain", floodBytes)
	}
}

func TestEmptyArgvIsRejected(t *testing.T) {
	if _, err := Start(context.Background(), Spec{}); err == nil {
		t.Fatal("Start with an empty argv must fail, not panic")
	}
}
