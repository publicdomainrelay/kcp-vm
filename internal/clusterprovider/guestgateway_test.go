//go:build linux

package clusterprovider

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

func guestStackConfig(t *testing.T, gateway func(string) (string, error)) StackConfig {
	t.Helper()
	cfg := stackConfig(t, fakeAdmin())
	cfg.GuestDialled = true
	cfg.AdvertiseAddress = ""
	cfg.GuestGateway = gateway
	return cfg
}

func observedCluster(name string) materialiser.Observed {
	o := observed()
	o.Cluster.Name = name
	return o
}

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

func TestAGuestClustersApiserverIsAdvertisedOnItsLeasedGateway(t *testing.T) {
	gateway := "10.100.0.1"
	argv := k3sArgv(t, guestStackConfig(t, func(string) (string, error) { return gateway, nil }),
		observed(), stackLeases(21003))
	if got := flagValue(argv, "--advertise-address"); got != gateway {
		t.Fatalf("--advertise-address is %q, want the cluster's leased gateway %q. A guest reaches "+
			"NONE of this host's addresses -- it sits behind a tap in its own network namespace and "+
			"its only path to the host is the gateway its default route names -- so an apiserver "+
			"advertised on any other address is one no guest can route to, and the node's own "+
			"kubelet, which reads the in-cluster kubernetes Service endpoint, would dial an address "+
			"that answers nothing", got, gateway)
	}
	if got := flagValue(argv, "--tls-san"); got != gateway {
		t.Fatalf("--tls-san is %q, want %q: the certificate must cover the address the node "+
			"actually connects to, or the join reads as an x509 name error rather than as a missing "+
			"SAN", got, gateway)
	}
	if got := flagValue(argv, "--bind-address"); got != portallocator.LoopbackAdvertiseAddress {
		t.Fatalf("--bind-address is %q, want loopback: the listener stays off the network, and "+
			"the gateway address is reached from the guest through pasta, never bound", got)
	}
}

func TestTheApiserverIsAdvertisedOnThePortItBinds(t *testing.T) {
	leases := stackLeases(21003)
	apiserver := leases[portallocator.PurposeK3sSecure].Port
	for _, tc := range []struct {
		name string
		cfg  StackConfig
	}{
		{"a cluster whose nodes are guests",
			guestStackConfig(t, func(string) (string, error) { return "10.100.0.1", nil })},
		{"a cluster whose nodes are local processes", stackConfig(t, fakeAdmin())},
	} {
		argv := k3sArgv(t, tc.cfg, observed(), leases)
		advertised := flagValue(argv, "--advertise-port")
		bound := flagValue(argv, "--https-listen-port")
		if bound != strconv.Itoa(apiserver) {
			t.Fatalf("%s: argv = %v names --https-listen-port %s and this cluster's apiserver "+
				"lease is %d", tc.name, argv, bound, apiserver)
		}
		if advertised != bound {
			t.Fatalf("%s: argv = %v advertises port %s and binds %s. --advertise-port is where "+
				"this cluster's apiserver is REACHABLE, so it is the port k3s binds its listener "+
				"on: the `kubernetes` Service endpoints carry it, every pod reaches the apiserver "+
				"through it, and the tunnel a guest's kubelet dials back to the apiserver is "+
				"dialled at it. MEASURED with the two flags at two separately leased ports: the "+
				"endpoints read <gateway>:%s, coredns never becomes Ready, metrics-server and "+
				"local-path-provisioner exit 2 within a second of starting, the apiserver's own "+
				"tunnel is refused with `dial tcp <gateway>:%s: connection refused`, and kubectl "+
				"logs answers 502", tc.name, argv, advertised, bound, advertised, advertised)
		}
	}
}

func TestTwoClustersDifferInTheAddressTheirGuestsDial(t *testing.T) {
	gateways := map[string]string{"alpha": "10.100.0.1", "beta": "10.100.1.1"}
	seen := map[string]string{}
	for _, cluster := range []string{"alpha", "beta"} {
		cfg := guestStackConfig(t, func(cluster string) (string, error) { return gateways[cluster], nil })
		argv := k3sArgv(t, cfg, observedCluster(cluster), stackLeases(21003))
		seen[cluster] = flagValue(argv, "--advertise-address")
	}
	if seen["alpha"] == seen["beta"] {
		t.Fatalf("both clusters advertise %q, so a node of one dials the other's apiserver: the "+
			"range is per cluster and the advertised address is that range's gateway, so this is "+
			"the collision the lease exists to refuse, arriving one layer up", seen["alpha"])
	}
	for cluster, want := range gateways {
		if seen[cluster] != want {
			t.Fatalf("%s advertises %q, want its own lease's gateway %q", cluster, seen[cluster], want)
		}
	}
}

func TestAGuestClusterWithNoLeasedGatewayIsRefused(t *testing.T) {
	_, err := StepsFor(guestStackConfig(t, nil))(observed(), stackLeases(21003))
	if err == nil {
		t.Fatal("a guest substrate with no gateway resolver built its steps: the address its " +
			"guests dial would then be whatever the operator's advertise address happened to be, " +
			"and a guest cannot route to any address this host carries")
	}
	for _, want := range []string{"guest", "gateway"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Fatalf("the refusal does not name %q, so it reads as a value that was not understood "+
				"rather than one that was understood and refused: %v", want, err)
		}
	}
}

func TestALeasedGatewayThatCannotBeResolvedIsNotSilentlySkipped(t *testing.T) {
	_, err := StepsFor(guestStackConfig(t, func(string) (string, error) {
		return "", errors.New("the subnet lease file is unreadable")
	}))(observed(), stackLeases(21003))
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("a lease that could not be read produced %v, and it must be reported rather than "+
			"swallowed: the alternative is an apiserver started on an address nobody leased", err)
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
