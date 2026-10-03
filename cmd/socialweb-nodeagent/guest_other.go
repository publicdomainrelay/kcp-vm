//go:build !linux

package main

import "fmt"

func InitGuest() error {
	return fmt.Errorf("socialweb-nodeagent: this binary's init verb is the guest's PID 1, and the " +
		"guest is always Linux even when the host that boots it is not: a Windows host runs QEMU, " +
		"which boots a Linux guest from this tree's Linux build. This non-Linux build carries the " +
		"host side of the agent only, so running init here would try to switch root into a root " +
		"filesystem that is not this process's and mount a /dev/vda that does not exist")
}

func GuestAgent() error {
	return fmt.Errorf("socialweb-nodeagent: this binary's agent verb mounts the node's identity " +
		"disk and becomes the k3s agent inside the guest, and the guest is always Linux even when " +
		"the host that boots it is not. This non-Linux build carries the host side of the agent " +
		"only, and on a Windows host the guest is booted from this tree's Linux build instead")
}
