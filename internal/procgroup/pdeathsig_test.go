//go:build linux

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

const (
	providerEnv     = "PROCGROUP_PROVIDER"
	providerPIDFile = "PROCGROUP_PROVIDER_PIDFILE"
)

func runProvider() int {
	runtime.LockOSThread()
	pidFile := os.Getenv(providerPIDFile)
	_ = os.Unsetenv(providerEnv)
	_ = os.Unsetenv(providerPIDFile)
	spawned := make(chan string, 1)
	go func() {
		runtime.LockOSThread()
		g, err := Spawn(context.Background(),
			[]Step{{Name: ComponentCloudControllerManager, Spec: spec(string(ComponentCloudControllerManager), ""), Ready: readyWhenAnnounced}},
			Options{PostgresUp: postgresUp})
		if err != nil {
			spawned <- "error " + err.Error()
			return
		}
		p, _ := g.Process(ComponentCloudControllerManager)
		spawned <- strconv.Itoa(p.PID)
	}()
	line := <-spawned
	if err := os.WriteFile(pidFile, []byte(line), 0o600); err != nil {
		return 2
	}
	if strings.HasPrefix(line, "error ") {
		return 3
	}
	time.Sleep(60 * time.Second)
	return 0
}

func childGone(pid int, start uint64) bool {
	now, err := hostos.ProcStart(pid)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return err == nil && now != start
}

func TestAGroupMemberDiesWithTheProviderAndNotWithTheThreadThatSpawnedIt(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	provider := exec.Command(os.Args[0], "-test.run=TestMain")
	provider.Env = append(os.Environ(), providerEnv+"=1", providerPIDFile+"="+pidFile)
	provider.Stdout, provider.Stderr = os.Stderr, os.Stderr
	if err := provider.Start(); err != nil {
		t.Fatalf("starting the stand-in provider: %v", err)
	}
	providerDone := make(chan struct{})
	go func() { _ = provider.Wait(); close(providerDone) }()
	t.Cleanup(func() {
		_ = provider.Process.Kill()
		<-providerDone
	})

	var line string
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw, err := os.ReadFile(pidFile)
		if err == nil && len(raw) > 0 {
			line = string(raw)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stand-in provider never reported the member it spawned")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pid, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("the stand-in provider could not spawn its group: %s", line)
	}
	start, err := hostos.ProcStart(pid)
	if err != nil {
		t.Fatalf("the member %d the provider spawned is not running: %v", pid, err)
	}
	t.Cleanup(func() {
		if !childGone(pid, start) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	time.Sleep(500 * time.Millisecond)
	if childGone(pid, start) {
		t.Fatalf("the member %d died while its provider is alive. Linux delivers PR_SET_PDEATHSIG "+
			"when the THREAD that forked the child exits, not the process, and the goroutine that "+
			"spawned this group was locked to its thread and returned, which ends the thread: a "+
			"member whose spawning thread is not held for its life dies at an arbitrary moment "+
			"of a healthy provider", pid)
	}

	if err := provider.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL to the stand-in provider: %v", err)
	}
	<-providerDone
	killed := time.Now()
	for !childGone(pid, start) {
		if time.Since(killed) > 5*time.Second {
			t.Fatalf("the member %d (%s) outlived its SIGKILLed provider by 5 s. A member that "+
				"binds no leased port and leaves no record -- the cloud controller manager -- is "+
				"invisible to the restart sweep, so a survivor here is one nothing on this host "+
				"will ever stop", pid, ComponentCloudControllerManager)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("reading: the member %d was alive 500 ms after its spawning thread exited, and gone %s "+
		"after its provider was SIGKILLed", pid, fmt.Sprint(time.Since(killed).Round(time.Millisecond)))
}
