package subnetallocator

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

type HostNetwork struct {
	Interface string
	Address   netip.Addr
	Prefix    netip.Prefix
}

func (h HostNetwork) String() string {
	return fmt.Sprintf("%s carried on interface %s as %s", h.Prefix, h.Interface, h.Address)
}

func HostNetworks() ([]HostNetwork, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing this host's interfaces: %w", err)
	}
	out := make([]HostNetwork, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("listing the addresses of interface %s: %w. A range is refused "+
				"because it overlaps a network this host carries, so an interface whose addresses "+
				"cannot be read is a set of networks no range can be compared against and a range "+
				"leased over it is a range nobody checked", iface.Name, err)
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				return nil, fmt.Errorf("interface %s reports an address of type %T, which is not "+
					"an address with a netmask on it. Overlap is refused against the whole network "+
					"an interface sits in rather than against the single address it answers on, "+
					"because a range sharing any part of that network is a range a guest can be "+
					"addressed in alongside this host, so an address without a mask is one this "+
					"package cannot refuse anything against", iface.Name, a)
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				return nil, fmt.Errorf("interface %s carries %v, which is not an IP address this "+
					"package can compare a range against", iface.Name, ipnet.IP)
			}
			ones, bits := ipnet.Mask.Size()
			if bits == 0 {
				return nil, fmt.Errorf("interface %s carries %s under netmask %v, which is not a "+
					"contiguous prefix and so cannot be written as a range of addresses. Every "+
					"refusal here compares two ranges, and a network that is not a range is one no "+
					"comparison covers", iface.Name, ipnet.IP, ipnet.Mask)
			}
			widest := addr.BitLen()
			if addr.Is4In6() {
				addr = addr.Unmap()
			}
			if bits == 128 && widest == 128 && addr.Is4() {
				if ones < 96 {
					return nil, fmt.Errorf("interface %s carries %s under a %d-bit netmask, which "+
						"is shorter than the IPv6 prefix an IPv4-mapped address arrives in, so the "+
						"network it sits in cannot be recovered from it", iface.Name, ipnet.IP, ones)
				}
				ones -= 96
				bits = 32
			}
			if bits != addr.BitLen() {
				return nil, fmt.Errorf("interface %s carries %s, an address %d bits wide, under a "+
					"%d-bit netmask, so the two do not describe one network and no range can be "+
					"compared against them", iface.Name, addr, addr.BitLen(), bits)
			}
			out = append(out, HostNetwork{
				Interface: iface.Name,
				Address:   addr,
				Prefix:    netip.PrefixFrom(addr, ones).Masked(),
			})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("this host reports no interface addresses at all, not even a " +
			"loopback address. Every range leased here is non-conflicting because it was compared " +
			"against the networks this host carries, and a host carrying none is indistinguishable " +
			"from a host whose interfaces could not be read -- so this refuses rather than treating " +
			"an unreadable host as an empty one and leasing over the network that made it unreadable")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prefix != out[j].Prefix {
			return out[i].Prefix.String() < out[j].Prefix.String()
		}
		return out[i].Interface < out[j].Interface
	})
	return out, nil
}

type HostCollisionError struct {
	Block   netip.Prefix
	Network HostNetwork
}

func (e *HostCollisionError) Error() string {
	return fmt.Sprintf("subnetallocator: refusing to lease %s: it overlaps %s. A range that overlaps "+
		"a network this host carries is not merely untidy. The guests of a cluster are tapped into "+
		"the network namespace pasta builds, and pasta builds that namespace by copying this host's "+
		"own network, so an address shared with %s puts a guest and this host's own interface on one "+
		"address and which of the two a packet belongs to is decided by whichever answered first. "+
		"That is the measured failure this package exists to remove: the host's own LAN address came "+
		"back as a node's InternalIP because the node's range was a copy of the host's LAN. This "+
		"host's networks are enumerated at every allocation rather than listed here, so this refusal "+
		"names the interface it actually collided with instead of a list that was true on one machine",
		e.Block, e.Network, e.Network.Prefix)
}

type LeaseCollisionError struct {
	Block netip.Prefix
	Held  Lease
}

func (e *LeaseCollisionError) Error() string {
	return fmt.Sprintf("subnetallocator: refusing to lease %s: it overlaps %s, which "+
		"cluster %s has held since %s. Two clusters on one range address their nodes identically. "+
		"The second cluster's node reports an address the first cluster's node also reports, so the "+
		"cluster's view of a node is a node of another cluster; each cluster's flannel is handed the "+
		"other's pod CIDR as its own; and a packet addressed to a guest of one arrives at a guest of "+
		"the other. Nothing in either cluster reports this, because every address involved is a legal "+
		"address -- they are simply not unique. The range is refused here or it is never refused at "+
		"all, which is why a range a human picks per cluster is dedicated by convention only",
		e.Block, e.Held.Block, e.Held.Cluster, e.Held.AllocatedAt.UTC().Format("2006-01-02T15:04:05Z"))
}

type OutOfPoolError struct {
	Block netip.Prefix
	Pool  Pool
}

func (e *OutOfPoolError) Error() string {
	return fmt.Sprintf("subnetallocator: %s is not a range of the pool %s. A range is "+
		"non-conflicting only because it was walked out of one pool and compared, block by block, "+
		"against one record of what is already leased; a range built anywhere else was compared "+
		"against nothing, which is the shape this package exists to take out of the design. The pool "+
		"is a constant in this package's code and not a setting: a pool two operators can set "+
		"differently is the same collision in a tidier place, and the collision this refuses is "+
		"invisible in both clusters when it happens", e.Block, e.Pool)
}

type PoolExhaustedError struct {
	Pool       Pool
	Candidates uint64
	HostSkips  uint64
	LeaseSkips uint64
	HostFirst  *HostCollisionError
	LeaseFirst *LeaseCollisionError
}

func (e *PoolExhaustedError) Error() string {
	detail := ""
	if e.HostFirst != nil {
		detail += fmt.Sprintf(" The first range refused for the host was: %v", e.HostFirst)
	}
	if e.LeaseFirst != nil {
		detail += fmt.Sprintf(" The first range refused for a lease already outstanding was: %v", e.LeaseFirst)
	}
	return fmt.Sprintf("subnetallocator: the pool %s is exhausted: every one of its %d ranges was "+
		"walked and not one is both free and non-conflicting, %d refused for overlapping a network "+
		"this host carries and %d for overlapping a range another cluster already holds. "+
		"Refusing rather than wrapping around to the first range, because a wrapped range is one a "+
		"cluster that is still running addresses its nodes in: the new cluster's nodes would be "+
		"addressed exactly as that cluster's nodes, both clusters would disagree about which of them "+
		"owns an address, and neither would report it. There is no range left to choose, so this is "+
		"not a choice an operator can make differently -- freeing a lease or widening the pool "+
		"constant are the two ways out.%s", e.Pool, e.Candidates, e.HostSkips, e.LeaseSkips, detail)
}

func FindDuplicateBlocks(leases []Lease) []string {
	byBlock := map[string][]Lease{}
	for _, l := range leases {
		key := l.Block.Masked().String()
		byBlock[key] = append(byBlock[key], l)
	}
	var out []string
	for block, holders := range byBlock {
		if len(holders) < 2 {
			continue
		}
		names := make([]string, 0, len(holders))
		for _, h := range holders {
			names = append(names, h.Cluster)
		}
		sort.Strings(names)
		out = append(out, fmt.Sprintf(
			"subnetallocator: range %s is held by BOTH %s. This is not a range conflict: two clusters "+
				"in one range address their nodes identically, so the second cluster's node reports "+
				"the first cluster's node's address as its InternalIP and a packet addressed to one "+
				"guest arrives at the other. Fix the duplicate LEASE -- do not go looking at the "+
				"nodes, whose addresses are legal and simply not unique",
			block, strings.Join(names, " and ")))
	}
	sort.Strings(out)
	return out
}

func (a *Allocator) checkLocked(block netip.Prefix, hosts []HostNetwork) error {
	if !block.IsValid() || !block.Addr().Is4() {
		return fmt.Errorf("subnetallocator: %v is not an IPv4 CIDR block, so there is no network to "+
			"compare and nothing to compare it with. Overlap is decided between two ranges, and a "+
			"value that is not a range is one that can neither conflict nor be proved not to", block)
	}
	for _, host := range hosts {
		if block.Overlaps(host.Prefix) {
			return &HostCollisionError{Block: block, Network: host}
		}
	}
	if block.Bits() != a.pool.Bits || !a.pool.Contains(block) {
		return &OutOfPoolError{Block: block, Pool: a.pool}
	}
	for _, held := range a.snapshot() {
		if block.Overlaps(held.Block.Masked()) {
			return &LeaseCollisionError{Block: block, Held: held}
		}
	}
	return nil
}
