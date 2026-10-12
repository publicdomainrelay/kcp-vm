//go:build linux

package devsup

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc/hostsvctest"
)

const (
	envTestState  = "SWC_DEVSUP_TEST_STATE"
	envTestSocket = "SWC_DEVSUP_TEST_SOCKET"
	envTestDie    = "SWC_DEVSUP_TEST_DIE_WITH"
	envTestStub   = "SWC_DEVSUP_TEST_STUB"
)

func TestMain(m *testing.M) {
	if hostsvctest.FDStoreHelper() {
		os.Exit(hostsvctest.FDStoreHelperMain())
	}
	if state := os.Getenv(envTestState); state != "" {
		die, _ := strconv.Atoi(os.Getenv(envTestDie))
		if os.Getenv(envTestStub) == "1" {
			os.Exit(runStub(os.Getenv(envTestSocket)))
		}
		if err := Run(context.Background(), Options{State: state, Socket: os.Getenv(envTestSocket), DieWith: die}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runStub(socket string) int {
	os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return 1
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return 1
		}
		go func() {
			defer c.Close()
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var req hostsvc.ProcRequest
			json.Unmarshal(line, &req)
			if req.Op == hostsvc.ProcOpHello {
				json.NewEncoder(c).Encode(hostsvc.ProcResponse{Hello: &hostsvc.ProcHello{Instance: "stub"}})
				return
			}
			json.NewEncoder(c).Encode(hostsvc.ProcResponse{Err: "stub devsup: " + req.Op + " is not built"})
		}()
	}
}

type devsupProc struct {
	dir    string
	socket string
	cmd    *exec.Cmd
	waited chan struct{}
	stub   bool
	die    int
}

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "swc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func launchDevsup(t *testing.T, dir string, stub bool) *devsupProc {
	return launchDevsupDieWith(t, dir, stub, os.Getpid())
}

func launchDevsupDieWith(t *testing.T, dir string, stub bool, die int) *devsupProc {
	t.Helper()
	d := &devsupProc{dir: dir, socket: filepath.Join(dir, "sup.sock"), stub: stub, die: die}
	d.start(t)
	t.Cleanup(func() {
		d.kill()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(d.dir, "devsup.log"))
			t.Logf("devsup log:\n%s", b)
		}
	})
	return d
}

func (d *devsupProc) start(t *testing.T) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), envTestState+"="+filepath.Join(d.dir, "sup"), envTestSocket+"="+d.socket,
		envTestDie+"="+strconv.Itoa(d.die))
	if d.stub {
		cmd.Env = append(cmd.Env, envTestStub+"=1")
	}
	log, err := os.OpenFile(filepath.Join(d.dir, "devsup.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d.cmd = cmd
	waited := make(chan struct{})
	d.waited = waited
	go func() {
		cmd.Wait()
		close(waited)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		if d.stub {
			var c net.Conn
			if c, err = net.Dial("unix", d.socket); err == nil {
				c.Close()
				return
			}
		} else {
			var m hostsvc.ProcClient
			if m, err = hostsvc.OpenProcClient(context.Background(), hostsvc.Config{ProcSocket: d.socket}); err == nil {
				h, herr := m.Hello(context.Background())
				if herr == nil && h.PID == cmd.Process.Pid {
					return
				}
				err = fmt.Errorf("hello %+v %v", h, herr)
			}
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(d.dir, "devsup.log"))
			t.Fatalf("devsup did not listen on %s: %v\n%s", d.socket, err, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d *devsupProc) kill() {
	if d.cmd != nil && d.cmd.Process != nil {
		d.cmd.Process.Signal(syscall.SIGKILL)
		select {
		case <-d.waited:
		case <-time.After(10 * time.Second):
		}
	}
}

func alivePID(pid int) bool {
	p, ok := readProcInfo(pid)
	return ok && p.state != 'Z' && p.state != 'X'
}

func (d *devsupProc) open(t *testing.T) (hostsvc.Manager, error) {
	return hostsvc.Open(context.Background(), hostsvc.Config{Backend: hostsvc.BackendProc, ProcSocket: d.socket})
}

func procHarness(t *testing.T, stub bool) hostsvctest.Harness {
	d := launchDevsup(t, shortDir(t), stub)
	key := hostsvctest.RandomKey()
	return hostsvctest.Harness{
		Open: d.open,
		RestartManager: func(t *testing.T) {
			d.kill()
			d.start(t)
		},
		Prefix:        "socialweb-test-" + key + "-",
		SlicePrefix:   "socialweb-test" + key,
		FDStoreHelper: fdStoreHelper,
	}
}

func fdStoreHelper(t *testing.T) ([]string, []string) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, "-test.run=^$"}, nil
}

func TestTheProcBackendPassesTheContract(t *testing.T) {
	hostsvctest.Run(t, procHarness(t, os.Getenv("SWC_DEVSUP_CONFORMANCE_STUB") == "1"))
}
