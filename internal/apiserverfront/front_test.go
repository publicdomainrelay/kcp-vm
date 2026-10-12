package apiserverfront

import (
	"bufio"
	"context"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/testdirs"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type namedBackend struct {
	name string
	ln   net.Listener
}

func startBackend(t *testing.T, name string) *namedBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &namedBackend{name: name, ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := io.WriteString(c, name+":"+line); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return b
}

func (b *namedBackend) addr() string { return b.ln.Addr().String() }

func startFront(t *testing.T, dir string) (*Front, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(ln, Options{StatePath: filepath.Join(dir, "front.json"), DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Serve(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return f, ln.Addr().String()
}

func ask(t *testing.T, c net.Conn, r *bufio.Reader, msg string) string {
	t.Helper()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, msg+"\n"); err != nil {
		t.Fatalf("write %q: %v", msg, err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read after %q: %v", msg, err)
	}
	return strings.TrimSpace(line)
}

func TestTheFrontPicksTheBackendPerNewConnectionAndLeavesOpenOnesAlone(t *testing.T) {
	a, b := startBackend(t, "a"), startBackend(t, "b")
	f, addr := startFront(t, testdirs.Short(t))
	if _, err := f.SetBackend(a.addr()); err != nil {
		t.Fatal(err)
	}
	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	r1 := bufio.NewReader(c1)
	if got := ask(t, c1, r1, "one"); got != "a:one" {
		t.Fatalf("first connection answered %q, want a:one", got)
	}
	st, err := f.SetBackend(b.addr())
	if err != nil {
		t.Fatal(err)
	}
	if st.Backend != b.addr() || st.Open[a.addr()] != 1 {
		t.Fatalf("after the flip: %+v, want backend %s and the open connection still on %s", st, b.addr(), a.addr())
	}
	if got := ask(t, c1, r1, "two"); got != "a:two" {
		t.Fatalf("the open connection moved: %q, want a:two", got)
	}
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if got := ask(t, c2, bufio.NewReader(c2), "three"); got != "b:three" {
		t.Fatalf("a new connection after the flip answered %q, want b:three", got)
	}
	c1.Close()
	deadline := time.Now().Add(5 * time.Second)
	for f.Status().Open[a.addr()] != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the closed connection is still counted on %s: %+v", a.addr(), f.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTheFrontNeverSwitchesByItselfWhenItsBackendDies(t *testing.T) {
	a, b := startBackend(t, "a"), startBackend(t, "b")
	f, addr := startFront(t, testdirs.Short(t))
	if _, err := f.SetBackend(a.addr()); err != nil {
		t.Fatal(err)
	}
	a.ln.Close()
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(c, "x\n")
		line, err := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if err == nil {
			t.Fatalf("a connection to a dead backend answered %q: the front failed over by itself", line)
		}
	}
	st := f.Status()
	if st.Backend != a.addr() || st.DialFailures < 3 {
		t.Fatalf("status %+v: want the dead backend %s kept and 3 dial failures counted", st, a.addr())
	}
	_ = b
}

func TestTheBackendSurvivesAFrontRestartThroughItsStateFile(t *testing.T) {
	a := startBackend(t, "a")
	dir := testdirs.Short(t)
	f1, _ := startFront(t, dir)
	if _, err := f1.SetBackend(a.addr()); err != nil {
		t.Fatal(err)
	}
	f2, addr := startFront(t, dir)
	if f2.Status().Backend != a.addr() {
		t.Fatalf("a restarted front came up with backend %q, want %s", f2.Status().Backend, a.addr())
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := ask(t, c, bufio.NewReader(c), "x"); got != "a:x" {
		t.Fatalf("got %q", got)
	}
}

func TestTheControlSocketSetsReportsAndDrains(t *testing.T) {
	a, b := startBackend(t, "a"), startBackend(t, "b")
	dir := testdirs.Short(t)
	f, addr := startFront(t, dir)
	sock := filepath.Join(dir, "front.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- ServeControl(ctx, sock, f, func() { ready <- nil }) }()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	cl := Client{Path: sock}
	st, err := cl.Set(ctx, a.addr())
	if err != nil || st.Backend != a.addr() || st.Address != addr {
		t.Fatalf("set: %+v %v", st, err)
	}
	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	r1 := bufio.NewReader(c1)
	ask(t, c1, r1, "one")
	if _, err := cl.Set(ctx, b.addr()); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Set(ctx, "not-an-address"); err == nil {
		t.Fatal("the front took a backend that is no host:port")
	}
	if st, _ := cl.Status(ctx); st.Backend != b.addr() {
		t.Fatalf("a refused set moved the backend: %+v", st)
	}
	n, err := cl.Drain(ctx, a.addr())
	if err != nil || n != 1 {
		t.Fatalf("drain closed %d (%v), want the one connection on %s", n, err, a.addr())
	}
	c1.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := r1.ReadString('\n'); err == nil {
		t.Fatal("a drained connection still reads")
	}
	if _, err := cl.Drain(ctx, b.addr()); err == nil {
		t.Fatal("draining the current backend must be refused")
	}
}
