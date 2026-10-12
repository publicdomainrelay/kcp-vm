//go:build linux

package apiserverfront

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func ListenerFromFD(fd int) (net.Listener, error) {
	unix.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), FDName)
	if f == nil {
		return nil, fmt.Errorf("apiserverfront: fd %d is not open", fd)
	}
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("apiserverfront: the inherited fd %d is no listening socket: %w", fd, err)
	}
	if _, ok := ln.(*net.TCPListener); !ok {
		ln.Close()
		return nil, fmt.Errorf("apiserverfront: the inherited fd %d is not a TCP listener", fd)
	}
	return ln, nil
}

func storeListener(sock string, ln net.Listener) error {
	tl, ok := ln.(*net.TCPListener)
	if !ok {
		return errors.New("apiserverfront: only a TCP listener goes into the fd store")
	}
	raw, err := tl.SyscallConn()
	if err != nil {
		return err
	}
	s, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("apiserverfront: a socket for the fd store at %s: %w", sock, err)
	}
	defer unix.Close(s)
	var werr error
	err = raw.Control(func(fd uintptr) {
		werr = unix.Sendmsg(s, []byte("FDSTORE=1\nFDNAME="+FDName), unix.UnixRights(int(fd)), &unix.SockaddrUnix{Name: sock}, 0)
	})
	if err != nil {
		return err
	}
	if werr != nil {
		return fmt.Errorf("apiserverfront: handing the listener to the fd store at %s: %w", sock, werr)
	}
	return nil
}
