//go:build !linux

package apiserverfront

import (
	"errors"
	"net"
	"runtime"
)

var errNoFDStore = errors.New("apiserverfront: no fd store on " + runtime.GOOS + " in this build")

func ListenerFromFD(fd int) (net.Listener, error) { return nil, errNoFDStore }

func HeldFromFD(fd int, name string) (*Held, error) { return nil, errNoFDStore }

func bindHold(name string, port int) (*Held, error) { return nil, errNoFDStore }

func storeListener(sock string, ln net.Listener) error { return errNoFDStore }

func storeHold(sock string, h *Held) error { return errNoFDStore }
