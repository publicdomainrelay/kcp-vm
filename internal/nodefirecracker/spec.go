package nodefirecracker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

const (
	VMLinuxName     = "vmlinux"
	InitramfsName   = "initramfs.cpio"
	RootfsImageName = "rootfs.ext4"
	GuestFileName   = "guest.yaml"

	HostBusybox = "/usr/bin/busybox"
)

const (
	SocketName    = nodevm.SocketName
	ConsoleName   = nodevm.ConsoleName
	InstanceName  = nodevm.InstanceName
	NamespaceName = nodevm.NamespaceName
	RootfsName    = nodevm.RootfsName
	NodeDiskName  = nodevm.NodeDiskName

	NodePasswordName = nodevm.NodePasswordName

	NamespaceLogName = nodevm.NamespaceLogName

	NodeFileName  = nodevm.NodeFileName
	NodeDiskLabel = nodevm.NodeDiskLabel

	GuestAgentPath  = nodevm.GuestAgentPath
	GuestFilePath   = nodevm.GuestFilePath
	GuestNodeDir    = nodevm.GuestNodeDir
	GuestTokenPath  = nodevm.GuestTokenPath
	GuestK3sBinary  = nodevm.GuestK3sBinary
	GuestK3sDataDir = nodevm.GuestK3sDataDir
	GuestAgentLog   = nodevm.GuestAgentLog

	TapName    = nodevm.TapName
	BridgeName = nodevm.BridgeName

	DefaultVcpu            = nodevm.DefaultVcpu
	DefaultMemMiB          = nodevm.DefaultMemMiB
	DefaultGuestMAC        = nodevm.DefaultGuestMAC
	DefaultInterface       = nodevm.DefaultInterface
	DefaultPasta           = nodevm.DefaultPasta
	DefaultShell           = nodevm.DefaultShell
	DefaultDiskMiB         = nodevm.DefaultDiskMiB
	DefaultInstanceTimeout = nodevm.DefaultInstanceTimeout

	NamespaceVerb = nodevm.NamespaceVerb
	AgentVerb     = nodevm.AgentVerb
	InitVerb      = nodevm.InitVerb
)

var ErrIncomplete = nodevm.ErrIncomplete

var ErrAlreadyRunning = errors.New(
	"nodefirecracker: this node already has a VMM running that this host did not start, so it was " +
		"started by a gateway that is gone -- starting a second VMM under the same node name puts two " +
		"guests behind one Node object, two kubelets reporting one node name, and two writers behind " +
		"one root filesystem; the record is cleared by Reap, never by adoption, because an adopted " +
		"VMM has two owners and a node that dies under one of them looks alive to the other")

var ErrNotRunning = errors.New(
	"nodefirecracker: no VMM of this node is running, and Running is the phase that says there is " +
		"one; writing it here would leave a node the scale decision counts as present with nothing " +
		"behind it, which is the direction that stops a cluster ever reaching zero")

type Image = nodevm.Image

type Guest = nodevm.Guest

type Network = nodevm.Network

type NodeDocument = nodevm.NodeDocument

type namespaceParams = nodevm.NamespaceParams

type instanceRecord = nodevm.InstanceRecord

type GuestDocument struct {
	Modules []string `yaml:"modules"`

	Interface string `yaml:"interface"`

	K3s GuestK3s `yaml:"k3s"`
}

type GuestK3s struct {
	Binary string `yaml:"binary"`

	DataDir string `yaml:"data_dir"`
}

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

	Firecracker string

	Pasta string

	Shell string

	Image Image

	Guest Guest

	Network Network

	DiskMiB int

	InstanceTimeout time.Duration

	NodeDiskStage func(ctx context.Context, fillDir, nodePassword string) error
}

func (o Options) withDefaults() Options {
	o.Guest = o.Guest.WithDefaults()
	o.Network = o.Network.WithDefaults()
	if o.Pasta == "" {
		o.Pasta = DefaultPasta
	}
	if o.Shell == "" {
		o.Shell = DefaultShell
	}
	if o.DiskMiB == 0 {
		o.DiskMiB = DefaultDiskMiB
	}
	if o.InstanceTimeout == 0 {
		o.InstanceTimeout = DefaultInstanceTimeout
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
	case o.Firecracker == "":
		return fmt.Errorf("%w: no firecracker binary, so there is no VMM to boot a guest in",
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

func SocketPath(dataDir string) string { return nodevm.SocketPath(dataDir) }

func ConsolePath(dataDir string) string { return nodevm.ConsolePath(dataDir) }

func InstancePath(dataDir string) string { return nodevm.InstancePath(dataDir) }

func NamespacePath(dataDir string) string { return nodevm.NamespacePath(dataDir) }

func NodeDiskPath(dataDir string) string { return nodevm.NodeDiskPath(dataDir) }

func RootfsPath(dataDir string) string { return nodevm.RootfsPath(dataDir) }

func NodePasswordPath(dataDir string) string { return nodevm.NodePasswordPath(dataDir) }

func Identity(dataDir string) []string {
	return []string{"firecracker", "--api-sock", SocketPath(dataDir)}
}

func NamespaceIdentity(dataDir string) []string {
	return []string{NamespaceVerb, NamespacePath(dataDir)}
}

func FindVMM(ctx context.Context, identity []string) (int, error) {
	return nodevm.FindVMM(ctx, identity)
}

func BuildNodeDisk(path string, doc NodeDocument, sizeMiB int, stage func(string) error) error {
	return nodevm.BuildNodeDisk(path, doc, sizeMiB, stage)
}

func CloneFile(src, dst string) error {
	return nodevm.CloneFile(src, dst)
}

func Fatal(dataDir string) string { return nodevm.Fatal(dataDir) }

func pastaArgv(opts Options, params string) []string {
	return nodevm.PastaArgv(opts.Pasta, opts.Shell, opts.Agent, opts.Network, params)
}
