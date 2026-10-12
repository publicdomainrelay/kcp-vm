//go:build linux

package apiserverfront

import (
	"bufio"
	"context"
	"errors"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/testdirs"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func receiveStored(t *testing.T, conn *net.UnixConn) (int, string) {
	t.Helper()
	buf := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4*4))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		t.Fatalf("the manager received no fd store message: %v", err)
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no control message with %q: %v", buf[:n], err)
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("rights %v %v", fds, err)
	}
	return fds[0], string(buf[:n])
}

func TestTheInheritedFDCountReadsListenFDsAndChecksListenPID(t *testing.T) {
	pid := strconv.Itoa(syscall.Getpid())
	cases := []struct {
		env  map[string]string
		want int
		err  bool
	}{
		{map[string]string{}, 0, false},
		{map[string]string{"LISTEN_FDS": "1"}, 1, false},
		{map[string]string{"LISTEN_FDS": "1", "LISTEN_PID": pid}, 1, false},
		{map[string]string{"LISTEN_FDS": "1", "LISTEN_PID": "1"}, 0, false},
		{map[string]string{"LISTEN_FDS": "x"}, 0, true},
		{map[string]string{"LISTEN_FDS": "2"}, 0, true},
	}
	for _, c := range cases {
		n, err := inheritedCount(func(k string) string { return c.env[k] })
		if n != c.want || (err != nil) != c.err {
			t.Errorf("%v: %d %v, want %d err=%v", c.env, n, err, c.want, c.err)
		}
	}
}

func TestAFrontRestartRefusesNoConnection(t *testing.T) {
	backend := startBackend(t, "k")
	dir := testdirs.Short(t)
	notify := filepath.Join(dir, "notify.sock")
	mgr, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: notify, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	env := map[string]string{EnvNotifySocket: notify}
	lnA, inherited, err := Acquire("127.0.0.1:0", true, func(k string) string { return env[k] })
	if err != nil || inherited {
		t.Fatalf("acquire: inherited=%v %v", inherited, err)
	}
	stored, msg := receiveStored(t, mgr)
	if msg != "FDSTORE=1\nFDNAME="+FDName {
		t.Fatalf("fd store message %q", msg)
	}
	addr := lnA.Addr().String()
	fA, err := New(lnA, Options{StatePath: filepath.Join(dir, "front.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fA.SetBackend(backend.addr()); err != nil {
		t.Fatal(err)
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	doneA := make(chan struct{})
	go func() { fA.Serve(ctxA); close(doneA) }()

	var refused, answered, broken atomic.Int64
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
				c, err := net.DialTimeout("tcp", addr, 2*time.Second)
				if err != nil {
					if errors.Is(err, syscall.ECONNREFUSED) {
						refused.Add(1)
					} else {
						broken.Add(1)
					}
					time.Sleep(2 * time.Millisecond)
					continue
				}
				c.SetDeadline(time.Now().Add(5 * time.Second))
				io.WriteString(c, "x\n")
				if _, err := bufio.NewReader(c).ReadString('\n'); err == nil {
					answered.Add(1)
				}
				c.Close()
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	time.Sleep(150 * time.Millisecond)
	cancelA()
	<-doneA
	gapStart := answered.Load()
	time.Sleep(300 * time.Millisecond)
	lnB, err := ListenerFromFD(stored)
	if err != nil {
		t.Fatal(err)
	}
	fB, err := New(lnB, Options{StatePath: filepath.Join(dir, "front.json"), Inherited: true})
	if err != nil {
		t.Fatal(err)
	}
	ctxB, cancelB := context.WithCancel(context.Background())
	doneB := make(chan struct{})
	go func() { fB.Serve(ctxB); close(doneB) }()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	cancelB()
	<-doneB
	if fB.Status().Address != addr || fB.Status().Backend != backend.addr() {
		t.Fatalf("the restarted front %+v, want %s -> %s", fB.Status(), addr, backend.addr())
	}
	t.Logf("answered %d (%d before the gap), refused %d, other dial errors %d", answered.Load(), gapStart, refused.Load(), broken.Load())
	if refused.Load() != 0 || broken.Load() != 0 {
		t.Fatalf("%d connections refused and %d failed to dial across the front restart", refused.Load(), broken.Load())
	}
	if answered.Load() <= gapStart {
		t.Fatalf("no connection answered after the restart")
	}
}
