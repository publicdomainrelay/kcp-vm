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
	DefaultInterface = nodevm.DefaultInterface
	DefaultPasta     = nodevm.DefaultPasta
)

type Image = nodevm.Image

type Guest = nodevm.Guest

type GuestDocument struct {
	Modules []string `yaml:"modules"`

	Interface string `yaml:"interface"`

	K3s GuestK3s `yaml:"k3s"`
}

type GuestK3s struct {
	Binary string `yaml:"binary"`

	DataDir string `yaml:"data_dir"`
}
