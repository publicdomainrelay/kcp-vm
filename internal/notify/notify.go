package notify

import (
	"net"
	"os"
	"strings"
)

const EnvSocket = "NOTIFY_SOCKET"

func Ready() error { return send("READY=1") }

func Watchdog() error { return send("WATCHDOG=1") }

func send(state string) error {
	addr := os.Getenv(EnvSocket)
	if addr == "" {
		return nil
	}
	c, err := net.Dial("unixgram", socketName(addr))
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(state))
	return err
}

func socketName(addr string) string {
	if strings.HasPrefix(addr, "@") {
		return "\x00" + addr[1:]
	}
	return addr
}
