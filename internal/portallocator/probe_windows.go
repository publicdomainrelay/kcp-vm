//go:build windows

package portallocator

import (
	"net"
)

var probeListen = net.ListenConfig{}
