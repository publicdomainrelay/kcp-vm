//go:build linux

package provider_test

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"
)

const liveReapTimeout = 10 * time.Second

type liveProcess struct {
	pid  int
	cmd  *exec.Cmd
	log  *os.File
	once sync.Once
}

var (
	liveMu      sync.Mutex
	liveProcs   []*liveProcess
	liveAborted bool
)

func (p *liveProcess) kill() {
	p.once.Do(func() {
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		_ = p.cmd.Wait()
		if p.log != nil {
			_ = p.log.Close()
		}
	})
}

func trackLiveProcess(t *testing.T, p *liveProcess) {
	t.Helper()
	liveMu.Lock()
	liveProcs = append(liveProcs, p)
	aborted := liveAborted
	liveMu.Unlock()
	t.Cleanup(p.kill)
	if aborted {
		p.kill()
	}
}

func reapLiveProcesses() {
	liveMu.Lock()
	liveAborted = true
	procs := append([]*liveProcess(nil), liveProcs...)
	liveMu.Unlock()

	for _, p := range procs {
		p.kill()
	}
	deadline := time.Now().Add(liveReapTimeout)
	for _, p := range procs {
		waitForGroupExit(p.pid, deadline)
	}
}

func waitForGroupExit(pid int, deadline time.Time) {
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ponytail: a test binary killed by a signal never runs t.Cleanup, so its kcp and kine process groups outlive it; every spawn registers here and this sweeps the registry on the way out, interrupt included.
func TestMain(m *testing.M) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		reapLiveProcesses()
		os.Exit(1)
	}()

	code := m.Run()
	reapLiveProcesses()
	os.Exit(code)
}
