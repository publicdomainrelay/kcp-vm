package nodefirecracker

import (
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

	GuestFilePath   = nodevm.GuestFilePath
	GuestK3sBinary  = nodevm.GuestK3sBinary
	GuestK3sDataDir = nodevm.GuestK3sDataDir

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
	InitVerb      = nodevm.InitVerb
)

var ErrIncomplete = nodevm.ErrIncomplete

type Image = nodevm.Image

type Guest = nodevm.Guest

type Network = nodevm.Network

type NodeDocument = nodevm.NodeDocument

type namespaceParams = nodevm.NamespaceParams

type GuestDocument struct {
	Modules []string `yaml:"modules"`

	Interface string `yaml:"interface"`

	K3s GuestK3s `yaml:"k3s"`
}

type GuestK3s struct {
	Binary string `yaml:"binary"`

	DataDir string `yaml:"data_dir"`
}

func SocketPath(dataDir string) string { return nodevm.SocketPath(dataDir) }

func ConsolePath(dataDir string) string { return nodevm.ConsolePath(dataDir) }

func InstancePath(dataDir string) string { return nodevm.InstancePath(dataDir) }

func NamespacePath(dataDir string) string { return nodevm.NamespacePath(dataDir) }

func NodeDiskPath(dataDir string) string { return nodevm.NodeDiskPath(dataDir) }

func RootfsPath(dataDir string) string { return nodevm.RootfsPath(dataDir) }

func NodePasswordPath(dataDir string) string { return nodevm.NodePasswordPath(dataDir) }

func CloneFile(src, dst string) error {
	return nodevm.CloneFile(src, dst)
}
