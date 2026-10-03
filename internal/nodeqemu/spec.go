package nodeqemu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

var ErrIncomplete = nodevm.ErrIncomplete

var ErrAlreadyRunning = errors.New(
	"nodeqemu: this node already has a QEMU process running that this host did not start, so it " +
		"was started by a gateway that is gone -- starting a second QEMU under the same node name " +
		"puts two guests behind one Node object, two kubelets reporting one node name, and two " +
		"writers behind one root filesystem; the record is cleared by Reap, never by adoption, " +
		"because an adopted VMM has two owners and a node that dies under one of them looks alive " +
		"to the other")

var ErrNotRunning = errors.New(
	"nodeqemu: no QEMU process of this node is running, and Running is the phase that says there " +
		"is one; writing it here would leave a node the scale decision counts as present with " +
		"nothing behind it, which is the direction that stops a cluster ever reaching zero")

type Options struct {
	Cluster string

	Server string

	NodeName string

	TokenFile string

	DataDir string

	Record func(ctx context.Context, nodeName string, phase v1alpha1.NodePhase) error

	Log *slog.Logger

	AuditKernel bool

	Agent string

	QEMU string

	Pasta string

	Shell string

	Image nodevm.Image

	Guest nodevm.Guest

	Network nodevm.Network

	DiskMiB int

	InstanceTimeout time.Duration

	NodeDiskStage func(ctx context.Context, fillDir, nodePassword string) error
}

func (o Options) withDefaults() Options {
	o.Guest = o.Guest.WithDefaults()
	o.Network = o.Network.WithDefaults()
	if o.Pasta == "" {
		o.Pasta = nodevm.DefaultPasta
	}
	if o.Shell == "" {
		o.Shell = nodevm.DefaultShell
	}
	if o.DiskMiB == 0 {
		o.DiskMiB = nodevm.DefaultDiskMiB
	}
	if o.InstanceTimeout == 0 {
		o.InstanceTimeout = nodevm.DefaultInstanceTimeout
	}
	return o
}

func (o Options) Validate() error {
	switch {
	case o.Cluster == "":
		return fmt.Errorf("%w: no cluster, and a node is per cluster", ErrIncomplete)
	case o.Server == "":
		return fmt.Errorf("%w: no server for the guest's agent to dial", ErrIncomplete)
	case o.NodeName == "":
		return fmt.Errorf("%w: no node name", ErrIncomplete)
	case o.TokenFile == "":
		return fmt.Errorf("%w: no token file, and the guest is handed the token as a file on its own "+
			"disk rather than as a value on a command line", ErrIncomplete)
	case o.DataDir == "":
		return fmt.Errorf("%w: no data dir, which is where this node's record, its own copy of the "+
			"root filesystem and its per-node disk live", ErrIncomplete)
	case o.Record == nil:
		return fmt.Errorf("%w: no Node-CR recorder, so this node's state would be visible to nobody "+
			"-- and the count the scale decision reads comes from those CRs", ErrIncomplete)
	case o.Agent == "":
		return fmt.Errorf("%w: no agent binary, which is the process pasta runs inside the guest's "+
			"network namespace and the process the guest's initramfs runs as PID 1; both sides are "+
			"one binary because the namespace half must see the same names the guest half does",
			ErrIncomplete)
	case o.QEMU == "":
		return fmt.Errorf("%w: no qemu-system binary, so there is no VMM to boot a guest in",
			ErrIncomplete)
	}
	if err := o.Image.Validate(); err != nil {
		return err
	}
	if err := o.Network.Validate(); err != nil {
		return err
	}
	return nodevm.ShellRunsAsPastasChild(o.Shell, o.Agent, o.DataDir)
}

func Identity(dataDir string) []string {
	return []string{"-name", filepath.Base(dataDir)}
}

func NamespaceIdentity(dataDir string) []string {
	return []string{nodevm.NamespaceVerb, nodevm.NamespacePath(dataDir)}
}
