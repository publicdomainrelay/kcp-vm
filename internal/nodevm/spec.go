package nodevm

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

const (
	SocketName    = "fc.sock"
	ConsoleName   = "guest.console"
	InstanceName  = "instance.json"
	NamespaceName = "namespace.json"
	RootfsName    = "rootfs.ext4"
	NodeDiskName  = "node-disk.ext4"

	NodePasswordName = "node-password"

	NamespaceLogName = "namespace.log"

	NodeFileName = "node.json"

	NodeDiskLabel   = "kcpnode"
	GuestAgentPath  = "/kcp/kcpagent"
	GuestFilePath   = "/kcp/guest.yaml"
	GuestNodeDir    = "/mnt/node"
	GuestTokenPath  = "/kcp/token"
	GuestK3sBinary  = "/usr/local/bin/k3s"
	GuestK3sDataDir = "/var/lib/rancher/k3s"
	GuestAgentLog   = "/kcp/agent.log"

	TapName    = "fctap0"
	BridgeName = "br0"

	DefaultVcpu            = 2
	DefaultMemMiB          = 2048
	DefaultGuestMAC        = "06:00:ac:10:00:02"
	DefaultInterface       = "eth0"
	DefaultPasta           = "pasta"
	DefaultShell           = "/bin/sh"
	DefaultDiskMiB         = 16
	DefaultInstanceTimeout = 90 * time.Second

	KindFirecracker = "firecracker"
	KindQEMU        = "qemu"

	VMMFirecracker = "firecracker"
	VMMQEMU        = "qemu"

	NamespaceVerb = "namespace"
	AgentVerb     = "agent"
	InitVerb      = "init"
)

func NormalizeVMM(name string) (string, error) {
	switch name {
	case "", VMMFirecracker:
		return VMMFirecracker, nil
	case VMMQEMU:
		return VMMQEMU, nil
	}
	return "", fmt.Errorf("nodevm: %q is not one of %s, %s. The name decides which VMM a node "+
		"is booted by, and it is written into the image a node boots from and read back before the "+
		"boot: a value nothing here understands would be carried as an image's provenance and "+
		"compared against a substrate that never produced it", name, VMMFirecracker, VMMQEMU)
}

var ErrIncomplete = errors.New(
	"nodevm: a node needs a cluster, the server it dials, its own name, the token file whose " +
		"CONTENT is written onto a per-node read-only disk, a data dir, the agent binary that becomes " +
		"the guest's PID 1 parent process, the VMM binary, and the three files the guest boots from; " +
		"the token VALUE is never one of them because it must not ride argv or a kernel command line, " +
		"and no port is one of them either because every port this node binds, its kubelet's " +
		"included, is bound inside the guest's own network namespace")

type Image struct {
	Kernel string

	Initramfs string

	Rootfs string
}

func (i Image) Validate() error {
	var problems []error
	for _, f := range []struct{ name, path string }{
		{"kernel", i.Kernel},
		{"initramfs", i.Initramfs},
		{"rootfs", i.Rootfs},
	} {
		if f.path == "" {
			problems = append(problems, fmt.Errorf("%w: the image names no %s", ErrIncomplete, f.name))
			continue
		}
		st, err := os.Stat(f.path)
		if err != nil {
			problems = append(problems, fmt.Errorf("the image's %s at %s: %w", f.name, f.path, err))
			continue
		}
		if st.IsDir() {
			problems = append(problems, fmt.Errorf("the image's %s at %s is a directory, so it "+
				"cannot be handed to the VMM as a file", f.name, f.path))
		}
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("nodevm: %w -- every one of the three is booted from, so a guest "+
			"missing any of them starts a VMM that exits before it can boot, which reads as a node "+
			"that never joined rather than as an image that was never built", err)
	}
	return nil
}

type Guest struct {
	Vcpu int

	MemMiB int

	SMP bool

	BootArgs []string
}

func (g Guest) WithDefaults() Guest {
	if g.Vcpu == 0 {
		g.Vcpu = DefaultVcpu
	}
	if g.MemMiB == 0 {
		g.MemMiB = DefaultMemMiB
	}
	return g
}

func (g Guest) CommandLine() string {
	return strings.Join(g.BootArgs, " ")
}

type Network struct {
	Gateway string

	GuestIP string

	Prefix int

	PastaIP string

	Tap string

	Bridge string

	GuestMAC string

	Interface string
}

func (n Network) Validate() error {
	switch {
	case n.Gateway == "" || n.GuestIP == "" || n.Prefix == 0 || n.PastaIP == "":
		return fmt.Errorf("%w: the range this node is addressed in is incomplete -- gateway %q, "+
			"address %q, prefix %d, pasta's own address %q. The first three were constants in this "+
			"package and they were a measurement of ONE machine: this host's own LAN, whose router "+
			"was handed to every guest as its default route and whose own interface address was "+
			"handed to a node as its address. Two consequences followed, both measured: a node's "+
			"InternalIP was the address of the machine running the gateway, and every cluster on "+
			"this host shared one /24, so 192.168.0.10 named a node in cluster A and a node in "+
			"cluster B alike. The range now comes from internal/subnetallocator, one range per "+
			"cluster, and a node built without one has no range at all rather than a borrowed one: "+
			"a borrowed range is at least visibly wrong, while a default here is a value nobody "+
			"chose that two clusters both get",
			ErrIncomplete, n.Gateway, n.GuestIP, n.Prefix, n.PastaIP)
	case n.Prefix < 1 || n.Prefix > 30:
		return fmt.Errorf("%w: this node is addressed with a /%d. A range holds the gateway pasta "+
			"hands the guest as its default route and at least one node's address, so the widest "+
			"range that leaves a node an address is a /30; the ranges this package is handed are "+
			"cut from a pool by internal/subnetallocator, which refuses to cut that pool into "+
			"anything wider", ErrIncomplete, n.Prefix)
	}
	gateway, err := netip.ParseAddr(n.Gateway)
	if err != nil {
		return fmt.Errorf("%w: the gateway %q is not an IP address, and it is what the guest's "+
			"default route names: pasta is handed it as --gateway, so a name here leaves the "+
			"guest with a default route that resolves to whichever address its own resolver "+
			"answers with, which in a guest whose resolver is written from this same document is "+
			"the address itself", ErrIncomplete, n.Gateway)
	}
	guest, err := netip.ParseAddr(n.GuestIP)
	if err != nil {
		return fmt.Errorf("%w: the address %q is not an IP address, and it is what the guest's "+
			"own interface is configured with -- the node's agent runs `ip addr add <address>/<prefix> "+
			"dev <interface>` from this document inside the guest", ErrIncomplete, n.GuestIP)
	}
	pasta, err := netip.ParseAddr(n.PastaIP)
	if err != nil {
		return fmt.Errorf("%w: the address %q that pasta's own end of the link carries is not an "+
			"IP address, and pasta is handed it as --address", ErrIncomplete, n.PastaIP)
	}
	if guest == gateway {
		return fmt.Errorf("%w: the node's address %s IS the gateway %s. The gateway is the address "+
			"the guest dials this host on and pasta answers the guest's ARP for, so a node given it "+
			"would be addressed as the thing it dials: the guest would ARP for itself, and the frame "+
			"it sent would be answered by the interface it was sent from", ErrIncomplete, n.GuestIP,
			n.Gateway)
	}
	if pasta == gateway {
		return fmt.Errorf("%w: pasta's own address %s IS the gateway %s. This is the shape that "+
			"was measured NOT to work: pasta replies to an ARP request for any address except the "+
			"one it holds itself -- its ARP handler discards a request whose target is its own "+
			"address -- so a guest whose default route names the address pasta carries gets no ARP "+
			"reply at all. The measured symptom is a node that never joins, with 'connect: no route "+
			"to host' in its own console: the ARP timed out, and no packet the guest sent ever "+
			"reached pasta. The two values must differ, and they differ by construction: the gateway "+
			"is the range's first usable address and pasta's is the range's own base, which is the "+
			"one address in the range the allocator never hands to a host",
			ErrIncomplete, n.PastaIP, n.Gateway)
	}
	range4 := netip.PrefixFrom(gateway, n.Prefix).Masked()
	for _, in := range []struct{ name, value string }{{"node address", n.GuestIP}, {"pasta's address", n.PastaIP}} {
		addr, err := netip.ParseAddr(in.value)
		if err != nil {
			return fmt.Errorf("%w: %s %q is not an IP address", ErrIncomplete, in.name, in.value)
		}
		if addr.Is4() && range4.Contains(addr) {
			continue
		}
		return fmt.Errorf("%w: %s %s is not inside %s, the range the gateway %s and the /%d "+
			"describe. Every address in it is handed out by the same lease, and a value from "+
			"outside it is one this node shares with a node of another cluster or with this host: "+
			"the guest would route every packet it sends to a member of a range it is not in",
			ErrIncomplete, in.name, in.value, range4, n.Gateway, n.Prefix)
	}
	return nil
}

func (n Network) WithDefaults() Network {
	if n.Tap == "" {
		n.Tap = TapName
	}
	if n.Bridge == "" {
		n.Bridge = BridgeName
	}
	if n.GuestMAC == "" {
		n.GuestMAC = DefaultGuestMAC
	}
	if n.Interface == "" {
		n.Interface = DefaultInterface
	}
	return n
}

// ponytail: user-mode networking has no pasta end and no leased range, so PastaIP is the range
// base only to satisfy Validate; give Network a Mode field if a second consumer must tell the two
// networks apart.
func UserNetwork(iface string) Network {
	return Network{
		Gateway:   hostos.UserGateway,
		GuestIP:   hostos.UserGuestIP,
		Prefix:    hostos.UserPrefix,
		PastaIP:   "10.0.2.0",
		Interface: iface,
	}
}

type NodeDocument struct {
	NodeName string `json:"nodeName"`

	Server string `json:"server"`

	Token string `json:"token"`

	NodePassword string `json:"nodePassword"`

	Address string `json:"address"`

	Prefix int `json:"prefix"`

	Gateway string `json:"gateway"`

	Interface string `json:"interface"`

	AuditKernel bool `json:"auditKernel"`
}

type NamespaceParams struct {
	Kind string `json:"kind"`

	VMM string `json:"vmm"`

	Name string `json:"name"`

	Kernel string `json:"kernel"`

	Initramfs string `json:"initramfs"`

	Rootfs string `json:"rootfs"`

	NodeDisk string `json:"nodeDisk"`

	BootArgs string `json:"bootArgs"`

	Socket string `json:"socket,omitempty"`

	Console string `json:"console"`

	Instance string `json:"instance"`

	Vcpu int `json:"vcpu"`

	MemMiB int `json:"memMiB"`

	SMP bool `json:"smp"`

	Tap string `json:"tap"`

	Bridge string `json:"bridge"`

	GuestMAC string `json:"guestMAC"`
}

type InstanceRecord struct {
	PID int `json:"pid"`

	StartNanos int64 `json:"startNanos"`
}

func ShellRunsAsPastasChild(shell, agent, dataDir string) error {
	if !filepath.IsAbs(shell) {
		return fmt.Errorf("%w: the shell pasta runs as its child is %q, which is not an absolute "+
			"path. That name is resolved inside pasta's child rather than here, so it would be "+
			"looked up in whatever directory that child inherited and the child would be whatever "+
			"file happened to be there, booting the guest on a process nobody chose",
			ErrIncomplete, shell)
	}
	info, err := os.Stat(shell)
	if err != nil {
		return fmt.Errorf("%w: the shell pasta runs as its child is %q and cannot be examined "+
			"(%v). That child is the process that adds the guest's tap and bridges it onto pasta's "+
			"own link, so a shell that cannot be started leaves the VMM never configured at all, "+
			"which is the same symptom as a guest that booted and joined nothing",
			ErrIncomplete, shell, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%w: the shell pasta runs as its child is %q and it is %s, which is not "+
			"an executable regular file, so pasta's child would fail to start for a reason that has "+
			"nothing to do with the namespace that child was given",
			ErrIncomplete, shell, info.Mode())
	}
	if agent != "" && SamePath(shell, agent) {
		return fmt.Errorf("%w: the shell pasta runs as its child is %q, which is this node's own "+
			"agent binary. That is the arrangement that failed before and cost the most: the agent "+
			"is a binary this host built and owns, pasta's child inherits pasta's own SELinux "+
			"domain, and in any domain other than unconfined_t that child is denied CAP_NET_ADMIN "+
			"and denied the write that would report the denial, so the failure arrives as an EMPTY "+
			"error and reads as plumbing. What this package relies on is not the label of the "+
			"program pasta execs but the probe in WireNamespace, which refuses to configure a VMM "+
			"until the namespace has been observed to carry pasta's own link; nothing here can see "+
			"the domain the gateway will be launched in, so a shell this host owns is refused "+
			"rather than trusted", ErrIncomplete, shell)
	}
	if dataDir != "" && InsideDir(dataDir, shell) {
		return fmt.Errorf("%w: the shell pasta runs as its child is %q, which is inside this "+
			"node's own data directory at %s, a tree this host creates and writes. Every reason a "+
			"user-owned binary is the wrong child applies to a file this host wrote: the child "+
			"inherits pasta's domain, and outside unconfined_t that domain withholds the "+
			"capability the child needs and withholds the write that would have said so, so the "+
			"error arrives empty and the guest never boots", ErrIncomplete, shell, dataDir)
	}
	return nil
}

func SamePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func InsideDir(dir, path string) bool {
	if dir == "" {
		return false
	}
	cleanDir := filepath.Clean(dir)
	cleanPath := filepath.Clean(path)
	return cleanPath != cleanDir &&
		strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator))
}

func SocketPath(dataDir string) string { return filepath.Join(dataDir, SocketName) }

func ConsolePath(dataDir string) string { return filepath.Join(dataDir, ConsoleName) }

func InstancePath(dataDir string) string { return filepath.Join(dataDir, InstanceName) }

func NamespacePath(dataDir string) string { return filepath.Join(dataDir, NamespaceName) }

func NodeDiskPath(dataDir string) string { return filepath.Join(dataDir, NodeDiskName) }

func RootfsPath(dataDir string) string { return filepath.Join(dataDir, RootfsName) }

func NodePasswordPath(dataDir string) string { return filepath.Join(dataDir, NodePasswordName) }
