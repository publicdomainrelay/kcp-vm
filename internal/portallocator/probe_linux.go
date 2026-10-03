//go:build linux

package portallocator

import (
	"net"
	"syscall"
)

var probeListen = net.ListenConfig{
	Control: func(network, address string, c syscall.RawConn) error {
		var inner error
		err := c.Control(func(fd uintptr) {
			inner = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 0)
		})
		if err != nil {
			return err
		}
		return inner
	},
}
