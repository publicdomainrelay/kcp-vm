//go:build darwin

package portallocator

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

var probeListen = net.ListenConfig{
	Control: func(network, address string, c syscall.RawConn) error {
		return fmt.Errorf("portallocator: probing %s %s is unsupported on darwin; see plan 0012: %w",
			network, address, errors.ErrUnsupported)
	},
}
