//go:build linux

package hostsvctest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/apiserverfront"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

const EnvFDStoreHelper = "SWC_FDSTORE_HELPER"

func FDStoreHelper() bool { return os.Getenv(EnvFDStoreHelper) == "1" }

func FDStoreHelperMain() int {
	ln, inherited, err := apiserverfront.Acquire("127.0.0.1:0", true, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "fd-store helper pid %d on %s inherited=%v\n", os.Getpid(), ln.Addr(), inherited)
	for {
		c, err := ln.Accept()
		if err != nil {
			return 1
		}
		fmt.Fprintf(c, "%d\n", os.Getpid())
		c.Close()
	}
}

func caseFDStoreAcrossACrash(t *testing.T, h Harness) {
	m := open(t, h)
	if !m.Capabilities().FDStore {
		t.Skipf("the %s backend declares FDStore=false", m.Capabilities().Backend)
	}
	if h.FDStoreHelper == nil {
		t.Fatalf("the %s harness names no fd-store helper argv", m.Capabilities().Backend)
	}
	argv, env := h.FDStoreHelper(t)
	spec := hostsvc.UnitSpec{Name: name(h, "fdstore-crash"), Exec: argv, Env: append(env, EnvFDStoreHelper+"=1"),
		FDStore: 1, Restart: hostsvc.RestartAlways, StopTimeout: 5 * time.Second}
	first := start(t, h, m, spec)
	c, cancel := ctxTimeout(10 * time.Second)
	defer cancel()
	port, err := hostsvc.WaitListenPort(c, first.MainPID, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("the helper bound no port: %v", err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	var refused, otherErr atomic.Int64
	pids := sync.Map{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
				if err != nil {
					if errors.Is(err, syscall.ECONNREFUSED) {
						refused.Add(1)
					} else {
						otherErr.Add(1)
					}
					time.Sleep(5 * time.Millisecond)
					continue
				}
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				line, err := bufio.NewReader(conn).ReadString('\n')
				conn.Close()
				if err == nil || errors.Is(err, io.EOF) {
					if pid, perr := strconv.Atoi(strings.TrimSpace(line)); perr == nil {
						pids.Store(pid, true)
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(first.MainPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	var again hostsvc.UnitStatus
	until(t, "the helper restarted on a new pid", 15*time.Second, func() bool {
		again = inspect(t, m, spec.Name)
		return again.State == hostsvc.Active && again.MainPID > 0 && again.MainPID != first.MainPID
	})
	until(t, "an answer from the restarted helper", 10*time.Second, func() bool {
		_, ok := pids.Load(again.MainPID)
		return ok
	})
	close(stop)
	wg.Wait()
	if p, err := hostsvc.ListenPort(again.MainPID); err != nil || p != port {
		t.Fatalf("the restarted helper listens on %d (%v), want the stored %d", p, err, port)
	}
	t.Logf("port %d kept across kill -9 of pid %d -> pid %d; refused %d, other dial errors %d",
		port, first.MainPID, again.MainPID, refused.Load(), otherErr.Load())
	if refused.Load() != 0 || otherErr.Load() != 0 {
		t.Fatalf("%d connections refused and %d failed across the crash", refused.Load(), otherErr.Load())
	}
}
