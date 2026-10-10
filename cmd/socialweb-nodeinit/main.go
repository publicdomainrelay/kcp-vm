package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

const usage = `socialweb-nodeinit - the guest's PID 1, from the initramfs

  socialweb-nodeinit            mounted as /init by the initramfs
  socialweb-nodeinit init       the same thing, named explicitly

The guest's kernel runs this as PID 1 before any root filesystem exists. It loads the transport
module the guest's block device arrives on, waits for /dev/vda, mounts it, moves the kernel's
/proc, /sys and /dev into it and switch_roots into /sbin/init. It is a Linux static build by
construction: a guest is always Linux, even when the host that boots it is not.

Joining the cluster is not this binary's business. A market guest is born from the requester's
cloud-init user_data, which brings up every unit that joins it.
`

func main() {
	if len(os.Args) < 2 || filepath.Base(os.Args[0]) == nodevm.InitVerb {
		fatalInit(InitGuest())
		return
	}
	switch os.Args[1] {
	case nodevm.InitVerb:
		fatalInit(InitGuest())
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

func fatalInit(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stdout, "KCP_INIT fatal: %v\n", err)
	powerOff()
}
