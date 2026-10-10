//go:build linux

package clusterprovider

import (
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func k3sArgv(t *testing.T, cfg StackConfig, o materialiser.Observed, leases materialiser.Leases) []string {
	t.Helper()
	steps, err := StepsFor(cfg)(o, leases)
	if err != nil {
		t.Fatalf("StepsFor: %v", err)
	}
	for _, s := range steps {
		if s.Name == procgroup.ComponentK3s {
			return s.Spec.Argv
		}
	}
	t.Fatalf("no k3s step in %v", names(steps))
	return nil
}

func TestTheApiserverIsAdvertisedOnThePortItBinds(t *testing.T) {
	leases := stackLeases(21003)
	apiserver := leases[portallocator.PurposeK3sSecure].Port
	argv := k3sArgv(t, stackConfig(t, fakeAdmin()), observed(), leases)
	advertised := flagValue(argv, "--advertise-port")
	bound := flagValue(argv, "--https-listen-port")
	if bound != strconv.Itoa(apiserver) {
		t.Fatalf("argv = %v names --https-listen-port %s and this cluster's apiserver "+
			"lease is %d", argv, bound, apiserver)
	}
	if advertised != bound {
		t.Fatalf("argv = %v advertises port %s and binds %s. --advertise-port is where "+
			"this cluster's apiserver is REACHABLE, so it is the port k3s binds its listener "+
			"on: the `kubernetes` Service endpoints carry it, every pod reaches the apiserver "+
			"through it, and the tunnel a node's kubelet dials back to the apiserver is "+
			"dialled at it. MEASURED with the two flags at two separately leased ports: the "+
			"endpoints read <address>:%s, coredns never becomes Ready, metrics-server and "+
			"local-path-provisioner exit 2 within a second of starting, the apiserver's own "+
			"tunnel is refused with `dial tcp <address>:%s: connection refused`, and kubectl "+
			"logs answers 502", argv, advertised, bound, advertised, advertised)
	}
}

func flagValue(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(arg, flag+"="); ok {
			return v
		}
	}
	return ""
}

func TestAMarketClustersApiserverIsAdvertisedOnAnAddressThisHostDoesNotCarry(t *testing.T) {
	cfg := stackConfig(t, fakeAdmin())
	cfg.MarketDialled = true
	cfg.AdvertiseAddress = portallocator.MarketAdvertiseAddress
	argv := k3sArgv(t, cfg, observed(), stackLeases(21003))
	if got := flagValue(argv, "--advertise-address"); got != portallocator.MarketAdvertiseAddress {
		t.Fatalf("--advertise-address is %q, want %q: the guest puts that address on its OWN "+
			"loopback and the market's door reverse-forwards it to the apiserver here",
			got, portallocator.MarketAdvertiseAddress)
	}
	if got := flagValue(argv, "--tls-san"); got != portallocator.MarketAdvertiseAddress {
		t.Fatalf("--tls-san is %q, want %q: the certificate must cover the address the node's "+
			"kubelet actually connects to, or the join reads as an x509 name error rather than as a "+
			"missing SAN", got, portallocator.MarketAdvertiseAddress)
	}
	if got := flagValue(argv, "--bind-address"); got != portallocator.LoopbackAdvertiseAddress {
		t.Fatalf("--bind-address is %q, want loopback: the market advertise address is not one this "+
			"host carries, so binding what is advertised would fail at bind rather than keep the "+
			"listener off the network", got)
	}
}

func TestAMarketClusterAnnouncesTheMarketPodNetworkAndNoOther(t *testing.T) {
	cfg := stackConfig(t, fakeAdmin())
	cfg.MarketDialled = true
	cfg.AdvertiseAddress = portallocator.MarketAdvertiseAddress
	argv := k3sArgv(t, cfg, observed(), stackLeases(21003))
	if got := flagValue(argv, "--flannel-backend"); got != "none" {
		t.Fatalf("--flannel-backend is %q, want none: a market node's pod network is the mesh's, "+
			"so k3s must not bring up flannel beside it", got)
	}
	if got := flagValue(argv, "--cluster-cidr"); got != "10.42.0.0/16" {
		t.Fatalf("--cluster-cidr is %q, want 10.42.0.0/16, the range the mesh addresses pods in", got)
	}
}
