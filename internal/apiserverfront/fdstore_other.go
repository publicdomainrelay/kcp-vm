//go:build !linux

package apiserverfront

import (
	"errors"
	"net"
	"runtime"
)

var errNoFDStore = errors.New("apiserverfront: no fd store on " + runtime.GOOS + " in this build")

func ListenerFromFD(fd int) (net.Listener, error) { return nil, errNoFDStore }

func storeListener(sock string, ln net.Listener) error { return errNoFDStore }
