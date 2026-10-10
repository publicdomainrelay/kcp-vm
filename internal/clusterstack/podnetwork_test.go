package clusterstack

import (
	"strings"
	"testing"
)

func market() Spec {
	s := valid()
	s.MarketDialled = true
	return s
}

func TestAMarketClusterRunsNoFlannelAndNamesItsPodNetwork(t *testing.T) {
	argv, err := market().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if !hasFlag(argv, "--flannel-backend", "none") {
		t.Fatalf("argv = %v\n\nwant --flannel-backend=none: market guests sit behind their own NAT, "+
			"so flannel's overlay has nothing to run over; socialweb-meshd carries pod traffic and "+
			"the guest's bridge CNI the node-local half", argv)
	}
	if !hasFlag(argv, "--cluster-cidr", MarketClusterCIDR) {
		t.Fatalf("argv = %v\n\nwant --cluster-cidr=%s: the guest's masquerade rule names this "+
			"range, so the server must name it rather than inherit k3s's default", argv, MarketClusterCIDR)
	}
	if MarketClusterCIDR != "10.42.0.0/16" {
		t.Fatalf("MarketClusterCIDR = %s; it must stay k3s's default, because a running cluster "+
			"cannot change its cluster CIDR", MarketClusterCIDR)
	}
}

func TestOnlyAMarketClusterDropsFlannel(t *testing.T) {
	for _, s := range []Spec{valid()} {
		argv, err := s.ServerArgv()
		if err != nil {
			t.Fatalf("ServerArgv: %v", err)
		}
		for _, a := range argv {
			if strings.HasPrefix(a, "--flannel-backend") || strings.HasPrefix(a, "--cluster-cidr") {
				t.Fatalf("a stack that dials no market carries %s: its nodes share one network and "+
					"keep flannel", a)
			}
		}
	}
}

func TestK3sAcceptsTheMarketPodNetworkFlags(t *testing.T) {
	bin := k3sOnPath(t)
	argv, err := market().ServerArgv()
	if err != nil {
		t.Fatalf("ServerArgv: %v", err)
	}
	if ok, out := k3sVerdict(bin, argv[2:]); !ok {
		t.Fatalf("k3s REFUSED the market argv:\n  %v\n\n%s", argv[2:], out)
	}
}
