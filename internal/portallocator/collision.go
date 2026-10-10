package portallocator

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

const LoopbackAdvertiseAddress = "127.0.0.1"

const MarketAdvertiseAddress = "100.127.255.254"

func FindDuplicatePorts(leases []Lease) []string {
	byPort := map[int][]Lease{}
	for _, l := range leases {
		width := l.Width
		if width < 1 {
			width = 1
		}
		for p := l.Port; p < l.Port+width; p++ {
			byPort[p] = append(byPort[p], l)
		}
	}
	ports := make([]int, 0, len(byPort))
	for p := range byPort {
		ports = append(ports, p)
	}
	sort.Ints(ports)

	var out []string
	for _, p := range ports {
		claimants := byPort[p]
		if len(claimants) < 2 {
			continue
		}
		names := make([]string, 0, len(claimants))
		for _, c := range claimants {
			names = append(names, fmt.Sprintf("%s (%s)", c.Cluster, c.Purpose))
		}
		out = append(out, fmt.Sprintf(
			"port %d is leased to BOTH %s. This is not a port conflict: a colliding advertise-port routes "+
				"this node's tunnel to ANOTHER CLUSTER'S apiserver, which surfaces as an x509 name error. "+
				"Fix the duplicate LEASE -- do not go looking at certificates",
			p, strings.Join(names, " and ")))
	}
	return out
}

type Endpoint struct {
	Address string
	Port    int
}

var dialLine = regexp.MustCompile(`wss://([A-Za-z0-9._-]+):(\d+)`)

var probeLine = regexp.MustCompile(`KCP_CONN apiserver ([A-Za-z0-9._-]+):(\d+) -> OK`)

func AssertAgentDialledOurPort(line string, want Endpoint) error {
	var m []string
	for _, re := range []*regexp.Regexp{probeLine, dialLine} {
		if found := re.FindStringSubmatch(line); found != nil {
			m = found
			break
		}
	}
	if m == nil {
		return fmt.Errorf("portallocator: no dial line from this node's agent in what it printed, " +
			"so which apiserver it reached is unverified. A silent log is a failure here, not a " +
			"pass: the agent being connected says nothing about WHICH cluster it reached, and a " +
			"guest that was handed the wrong address prints a FAIL line of the same shape")
	}
	gotPort, err := strconv.Atoi(m[2])
	if err != nil {
		return fmt.Errorf("portallocator: unparsable dial line %q", line)
	}
	if m[1] != want.Address || gotPort != want.Port {
		return fmt.Errorf("portallocator: ADVERTISE MISMATCH: the agent dialled %s:%d but this cluster's lease is %s:%d. "+
			"If the ports differ, a colliding advertise-port routes this node's tunnel to ANOTHER CLUSTER'S apiserver, "+
			"which surfaces as an x509 name error -- the certificate is not the fault",
			m[1], gotPort, want.Address, want.Port)
	}
	return nil
}

func AssertAdvertiseAddress(address string) error {
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("portallocator: --advertise-address %q is not an IP address. It is the "+
			"address a NODE dials this cluster's apiserver on, so a name here resolves to whatever "+
			"the node's resolver says, which is not a value this host owns", address)
	}
	for _, iface := range hostAddresses() {
		if iface.Equal(ip) {
			return nil
		}
	}
	return fmt.Errorf("portallocator: --advertise-address %q is not an address this host carries, "+
		"so a node that is a LOCAL PROCESS told to dial it dials nothing. This was measured: the "+
		"deleted VM substrate's constant 192.168.5.2 has no interface here, nothing answers it, and "+
		"the store DSN built from it never connected. That rule does NOT hold for a node that is a "+
		"guest, and it is inverted there: a guest sits behind a tap in its own network namespace and "+
		"cannot route to ANY address this host carries -- it reaches the host's apiserver through "+
		"the gateway its own default route points at. A guest's dial address is the substrate's, so "+
		"it is checked by AssertGuestDialAddress and then proved by AssertAgentDialledOurPort from "+
		"the node's own dial, not by this host's interface list",
		address)
}

func AssertGuestDialAddress(address string) error {
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("portallocator: %q is not an IP address, and it is the address this "+
			"cluster's GUEST nodes dial its apiserver on. A name here resolves to whatever each "+
			"guest's resolver says, which is a different answer in every guest and no answer at all "+
			"in a guest whose resolver has not been written yet", address)
	}
	for _, iface := range hostAddresses() {
		if iface.Equal(ip) {
			return fmt.Errorf("portallocator: %q is an address THIS HOST carries, and a guest "+
				"cannot route to it: the guest's only path to the host is the gateway its default "+
				"route names, so an address on the host's own interfaces is one the guest's packets "+
				"never arrive at. The value a guest dials belongs to the substrate that gives the "+
				"guest its network, which for the firecracker substrate is the pasta gateway, and "+
				"the evidence that it is right is the node's own dial line -- AssertAgentDialledOurPort "+
				"-- rather than any property of this host", address)
		}
	}
	return nil
}

func AssertMarketAdvertiseAddress(address string) error {
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("portallocator: %q is not an IP address, and it is the address a MARKET "+
			"node dials its cluster's apiserver on. A name here resolves to whatever each guest's "+
			"resolver says, which is a different answer in every guest and no answer at all in a "+
			"guest whose resolver has not been written yet", address)
	}
	if ip.IsLoopback() {
		return fmt.Errorf("portallocator: %q is a loopback address, and a market node's machine is "+
			"not this host. k3s refuses a loopback address for the in-cluster `kubernetes` Service "+
			"endpoint (measured: subsets[0].addresses[0].ip Invalid value: \"127.0.0.1\": may not be "+
			"in the loopback range), so a cluster advertised on one fails to start rather than fails "+
			"to be reached", address)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("portallocator: %q is the unspecified address, which names every "+
			"interface and no interface. A market node puts the advertised address on its OWN "+
			"loopback and the door reverse-forwards it here, so there is nothing for a wildcard to "+
			"be put on and nothing for the forward to name", address)
	}
	for _, iface := range hostAddresses() {
		if iface.Equal(ip) {
			return fmt.Errorf("portallocator: %q is an address THIS HOST carries, and a market "+
				"node's machine is on the far side of the market's SSH door. The guest puts the "+
				"advertised address on its OWN loopback and the door reverse-forwards it to this "+
				"host's apiserver, so an address this host already carries is one the guest cannot "+
				"take and the forward collides with whatever answers it here", address)
		}
	}
	return nil
}

func AssertAdvertiseInvariant(advertiseAddress string, advertisePort, forwardHostPort int, lease Lease) error {
	return advertiseInvariant(advertiseAddress, advertisePort, forwardHostPort, lease, dialFromThisHost)
}

func AssertGuestAdvertiseInvariant(advertiseAddress string, advertisePort, forwardHostPort int, lease Lease) error {
	return advertiseInvariant(advertiseAddress, advertisePort, forwardHostPort, lease, dialFromAGuest)
}

func AssertMarketAdvertiseInvariant(advertiseAddress string, advertisePort, forwardHostPort int, lease Lease) error {
	return advertiseInvariant(advertiseAddress, advertisePort, forwardHostPort, lease, dialFromTheMarket)
}

type dialler uint8

const (
	dialFromThisHost dialler = iota

	dialFromAGuest

	dialFromTheMarket
)

func advertiseInvariant(advertiseAddress string, advertisePort, forwardHostPort int, lease Lease, dial dialler) error {
	check := AssertAdvertiseAddress
	switch dial {
	case dialFromAGuest:
		check = AssertGuestDialAddress
	case dialFromTheMarket:
		check = AssertMarketAdvertiseAddress
	}
	if err := check(advertiseAddress); err != nil {
		return err
	}
	if lease.Purpose != PurposeK3sSecure {
		return fmt.Errorf("portallocator: cluster %s holds %s:%d, and that is not the lease whose port "+
			"the apiserver is advertised on. The advertised port is where the apiserver is reachable, "+
			"so it is the port k3s binds its listener on -- %s -- and a second lease for the "+
			"advertisement is a lease nothing binds",
			lease.Cluster, lease.Purpose, lease.Port, PurposeK3sSecure)
	}
	if advertisePort != lease.Port {
		return fmt.Errorf("portallocator: refusing to start: --advertise-port is %d but cluster %s's "+
			"apiserver lease is %d. The port a cluster advertises is the port everything in the "+
			"cluster reaches it on: the `kubernetes` Service endpoints carry it, and the tunnel a "+
			"guest's kubelet dials back to the apiserver is dialled at it. Announcing a port nothing "+
			"binds leaves coredns, metrics-server and local-path-provisioner dialling a closed port "+
			"and the apiserver's own log proxy refused, which surfaces as a refused dial and a 502 "+
			"from kubectl logs rather than as a misconfigured flag",
			advertisePort, lease.Cluster, lease.Port)
	}
	if forwardHostPort != lease.Port {
		return fmt.Errorf("portallocator: refusing to start: the advertise forward is bound to host %d but cluster %s's lease is %d; "+
			"the forward and the advertised port must come from the same lease", forwardHostPort, lease.Cluster, lease.Port)
	}
	return nil
}

func hostAddresses() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return []net.IP{net.ParseIP(LoopbackAdvertiseAddress)}
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		switch v := a.(type) {
		case *net.IPNet:
			out = append(out, v.IP)
		case *net.IPAddr:
			out = append(out, v.IP)
		}
	}
	return out
}

func AssertRangeDisjoint(carve, ephemeral Range) error {
	if ephemeral.Empty() || !carve.Overlaps(ephemeral) {
		return nil
	}
	return &RangeConflictError{
		Range:  carve,
		Reason: "intersects the OS ephemeral range, so the kernel will hand out ports we believe we hold",
		Detail: fmt.Sprintf("measured live ephemeral range is %s; move the carve below it rather than narrowing silently", ephemeral),
	}
}

func ReadEphemeralRange() (Range, error) {
	first, last, err := hostos.LocalPortRange()
	if err != nil {
		return Range{}, fmt.Errorf("portallocator: %w", err)
	}
	return Range{first, last}, nil
}

func probeBound(port int) bool {
	conn, err := probeListen.Listen(context.Background(), "tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return true
	}
	conn.Close()
	return false
}
