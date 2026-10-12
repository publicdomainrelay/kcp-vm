//go:build !linux

package guestnet

import "errors"

func Wire(w Wiring) error {
	return errors.New("guestnet: wiring a guest's namespace is Linux only")
}
