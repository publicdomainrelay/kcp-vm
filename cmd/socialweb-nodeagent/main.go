package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeqemu"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

const usage = `socialweb-nodeagent - the process on both sides of a node's network

  socialweb-nodeagent namespace <params.json>   runs inside the guest's network namespace as the
                                                child of pasta: it wires the guest's tap onto
                                                pasta's own link and starts the VMM
  socialweb-nodeagent init                      the guest's PID 1, from the initramfs
  socialweb-nodeagent agent                     mounts this node's identity disk and becomes
                                                the k3s agent that joins the cluster

namespace is the host side. init and agent are the guest side. They are one binary because the
guest's half must be a static build for the guest while the host's half must run inside the
namespace pasta made, and one artifact keeps the names on both sides in step.
`

func main() {
	if len(os.Args) < 2 || filepath.Base(os.Args[0]) == nodefirecracker.InitVerb {
		fatalInit(InitGuest())
		return
	}
	var err error
	switch os.Args[1] {
	case nodefirecracker.InitVerb:
		fatalInit(InitGuest())
		return
	case nodefirecracker.AgentVerb:
		if err = GuestAgent(); err != nil {
			fmt.Fprintf(os.Stdout, "KCP_AGENT fatal: %v\n", err)
		}
	case nodefirecracker.NamespaceVerb:
		if len(os.Args) < 3 {
			err = fmt.Errorf("the namespace verb needs the path to the parameters its parent wrote: " +
				"this process is pasta's child, so it has no other way to learn which kernel, which " +
				"disks and which tap it is here to connect")
			break
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = nodevm.RunNamespace(ctx, os.Args[2], map[string]nodevm.Runner{
			nodevm.KindFirecracker: nodefirecracker.RunVMM,
			nodevm.KindQEMU:        nodeqemu.RunVMM,
		})
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-nodeagent: %v\n", err)
		os.Exit(1)
	}
}

func fatalInit(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stdout, "KCP_INIT fatal: %v\n", err)
	powerOff()
}
