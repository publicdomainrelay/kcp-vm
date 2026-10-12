package apiserverfront

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

const (
	EnvNotifySocket = "NOTIFY_SOCKET"
	EnvListenFDs    = "LISTEN_FDS"
	EnvListenPID    = "LISTEN_PID"
	EnvListenNames  = "LISTEN_FDNAMES"
	FDName          = "front"
	listenFDStart   = 3
)

func inheritedCount(getenv func(string) string) (int, error) {
	v := getenv(EnvListenFDs)
	if v == "" {
		return 0, nil
	}
	if pid := getenv(EnvListenPID); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("apiserverfront: %s=%q is no count", EnvListenFDs, v)
	}
	if n > 1 {
		return 0, fmt.Errorf("apiserverfront: %s=%d: the front holds exactly one listening socket", EnvListenFDs, n)
	}
	return n, nil
}

func Acquire(address string, fdStore bool, getenv func(string) string) (net.Listener, bool, error) {
	n, err := inheritedCount(getenv)
	if err != nil {
		return nil, false, err
	}
	for _, k := range []string{EnvListenFDs, EnvListenPID, EnvListenNames} {
		os.Unsetenv(k)
	}
	if n == 1 {
		ln, err := ListenerFromFD(listenFDStart)
		if err != nil {
			return nil, false, err
		}
		return ln, true, nil
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, false, fmt.Errorf("apiserverfront: listening on %s: %w", address, err)
	}
	if fdStore {
		sock := getenv(EnvNotifySocket)
		if sock == "" {
			ln.Close()
			return nil, false, fmt.Errorf("apiserverfront: an fd-store front needs %s from its service manager", EnvNotifySocket)
		}
		if err := storeListener(sock, ln); err != nil {
			ln.Close()
			return nil, false, err
		}
	}
	return ln, false, nil
}
