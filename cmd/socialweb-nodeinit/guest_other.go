//go:build !linux

package main

import "fmt"

func InitGuest() error {
	return fmt.Errorf("socialweb-nodeinit: this binary is the guest's PID 1, and the guest is " +
		"always Linux even when the host that boots it is not: a Windows host runs a VMM, which " +
		"boots a Linux guest from this tree's Linux build. Running init on a non-Linux host would " +
		"try to switch root into a root filesystem that is not this process's and mount a /dev/vda " +
		"that does not exist")
}
