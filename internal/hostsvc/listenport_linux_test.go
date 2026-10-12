//go:build linux

package hostsvc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func fakeProc(t *testing.T, pid string, sockets []string, tcp, tcp6 string) string {
	t.Helper()
	root := t.TempDir()
	fd := filepath.Join(root, pid, "fd")
	if err := os.MkdirAll(fd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, pid, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, s := range sockets {
		if err := os.Symlink(s, filepath.Join(fd, string(rune('3'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, pid, "net", "tcp"), []byte(tcpHeader+tcp), 0o644)
	os.WriteFile(filepath.Join(root, pid, "net", "tcp6"), []byte(tcpHeader+tcp6), 0o644)
	return root
}

const (
	rowListenMine  = "   0: 0100007F:9195 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 4242 1 0000000000000000 100 0 0 10 0\n"
	rowListenOther = "   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 7777 1 0000000000000000 100 0 0 10 0\n"
	rowEstablished = "   2: 0100007F:9196 0100007F:9195 01 00000000:00000000 00:00000000 00000000  1000        0 4343 1 0000000000000000 100 0 0 10 0\n"
	rowListenSix   = "   0: 00000000000000000000000001000000:A000 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 5151 1 0000000000000000 100 0 0 10 0\n"
)

func TestListenPortReadsTheOneListenSocketOfTheMainPID(t *testing.T) {
	root := fakeProc(t, "42", []string{"socket:[4242]", "socket:[4343]", "/dev/null"}, rowListenMine+rowListenOther+rowEstablished, "")
	port, err := listenPortIn(root, 42)
	if err != nil || port != 0x9195 {
		t.Fatalf("port %d err %v, want %d", port, err, 0x9195)
	}
}

func TestListenPortRefusesAUnitWithTwoListenSockets(t *testing.T) {
	root := fakeProc(t, "42", []string{"socket:[4242]", "socket:[5151]"}, rowListenMine, rowListenSix)
	_, err := listenPortIn(root, 42)
	if !errors.Is(err, ErrManyListeners) {
		t.Fatalf("err %v, want ErrManyListeners", err)
	}
}

func TestListenPortIgnoresAnotherProcessesSockets(t *testing.T) {
	root := fakeProc(t, "42", []string{"socket:[9999]"}, rowListenMine+rowListenOther, "")
	_, err := listenPortIn(root, 42)
	if !errors.Is(err, ErrNoListener) {
		t.Fatalf("err %v, want ErrNoListener", err)
	}
}

func TestListenPortReadsThisProcessesRealPortZeroListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port, err := ListenPort(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if want := l.Addr().(*net.TCPAddr).Port; port != want {
		t.Fatalf("read back %d, bound %d", port, want)
	}
}
