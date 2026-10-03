package subnetallocator

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var thisHost = []HostNetwork{
	{Interface: "lo", Address: netip.MustParseAddr("127.0.0.1"), Prefix: netip.MustParsePrefix("127.0.0.0/8")},
	{Interface: "enp8s0", Address: netip.MustParseAddr("192.168.0.20"), Prefix: netip.MustParsePrefix("192.168.0.0/24")},
	{Interface: "tailscale0", Address: netip.MustParseAddr("100.90.139.88"), Prefix: netip.MustParsePrefix("100.90.139.88/32")},
	{Interface: "docker0", Address: netip.MustParseAddr("172.17.0.1"), Prefix: netip.MustParsePrefix("172.17.0.0/16")},
	{Interface: "br-1a2b", Address: netip.MustParseAddr("172.18.0.1"), Prefix: netip.MustParsePrefix("172.18.0.0/16")},
	{Interface: "br-3c4d", Address: netip.MustParseAddr("172.19.0.1"), Prefix: netip.MustParsePrefix("172.19.0.0/16")},
}

func fakeHost() ([]HostNetwork, error) { return thisHost, nil }

func tinyPool() Pool {
	return Pool{Base: netip.MustParsePrefix("10.200.0.0/26"), Bits: 29}
}

func withAllocator(t *testing.T, pool Pool, fn func(a *Allocator, path string)) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "networks.json")
	a, err := openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, pool)
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	fn(a, path)
}

func reopenWith(t *testing.T, path string, pool Pool) *Allocator {
	t.Helper()
	a, err := openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, pool)
	if err != nil {
		t.Fatalf("openWithPool(restart): %v", err)
	}
	return a
}

func mustAllocate(t *testing.T, a *Allocator, cluster string) Lease {
	t.Helper()
	l, err := a.Allocate(Request{Cluster: cluster})
	if err != nil {
		t.Fatalf("Allocate(%s): %v", cluster, err)
	}
	return l
}

func TestAFreshClusterIsHandedARangeWithNoHumanActionAndTheNextOneIsDifferent(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, _ string) {
		first := mustAllocate(t, a, "alpha")
		second := mustAllocate(t, a, "beta")

		if first.Block.Masked() != netip.MustParsePrefix("10.100.0.0/24") {
			t.Fatalf("a fresh cluster holding no lease was handed %s. Nothing but the cluster's name "+
				"was supplied -- no range, no offset, no index -- so the pool's first range is the "+
				"only answer the code can have produced", first.Block)
		}
		if second.Block.Masked() == first.Block.Masked() {
			t.Fatalf("two clusters were both handed %s. This is the whole failure: two clusters in "+
				"one range address their nodes identically and neither reports it", first.Block)
		}
		if first.Block.Overlaps(second.Block) {
			t.Fatalf("cluster alpha holds %s and cluster beta holds %s, and the two overlap",
				first.Block, second.Block)
		}
		if second.Block.Masked() != netip.MustParsePrefix("10.100.1.0/24") {
			t.Fatalf("the second cluster was handed %s, which is not the first range the walk found "+
				"free", second.Block)
		}
	})
}

func TestARangeOverlappingANetworkThisHostCarriesIsRefusedAndNamesTheInterface(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, _ string) {
		for _, tc := range []struct {
			block     string
			iface     string
			hostRange string
		}{
			{"192.168.0.0/24", "enp8s0", "192.168.0.0/24"},
			{"172.17.0.0/24", "docker0", "172.17.0.0/16"},
			{"172.18.0.0/24", "br-1a2b", "172.18.0.0/16"},
			{"127.0.5.0/24", "lo", "127.0.0.0/8"},
		} {
			err := a.Check(netip.MustParsePrefix(tc.block))
			var collide *HostCollisionError
			if !errors.As(err, &collide) {
				t.Fatalf("Check(%s) = %v, want a refusal because this host carries it", tc.block, err)
			}
			if !strings.Contains(err.Error(), tc.iface) {
				t.Fatalf("the refusal for %s does not name the interface it collided with, so an "+
					"operator reading it cannot tell what to move: %v", tc.block, err)
			}
			if !strings.Contains(err.Error(), tc.hostRange) {
				t.Fatalf("the refusal for %s does not name the network it collided with: %v",
					tc.block, err)
			}
			if collide.Network.Interface != tc.iface {
				t.Fatalf("the refusal names interface %q, want %q", collide.Network.Interface, tc.iface)
			}
		}
	})
}

func TestAPoolPartiallyCoveredByTheHostKeepsTheRestOfItsRanges(t *testing.T) {
	pool := Pool{Base: netip.MustParsePrefix("10.100.0.0/16"), Bits: 24}
	host := []HostNetwork{
		{Interface: "enp8s0", Address: netip.MustParseAddr("10.100.0.1"), Prefix: netip.MustParsePrefix("10.100.0.0/24")},
		{Interface: "docker0", Address: netip.MustParseAddr("10.100.1.1"), Prefix: netip.MustParsePrefix("10.100.1.0/24")},
	}
	dir := t.TempDir()
	a, err := openWithPool(Config{
		LeasePath:    filepath.Join(dir, "networks.json"),
		HostNetworks: func() ([]HostNetwork, error) { return host, nil },
	}, pool)
	if err != nil {
		t.Fatalf("openWithPool: %v -- a host network inside the pool costs the ranges it covers and "+
			"nothing else, so the pool is still openable", err)
	}
	lease := mustAllocate(t, a, "alpha")
	if lease.Block.Masked() != netip.MustParsePrefix("10.100.2.0/24") {
		t.Fatalf("cluster alpha was handed %s. The first two ranges of the pool are networks this "+
			"host already carries, so the walk must skip them and take the third -- skipping is not "+
			"the same as invalidating the pool, which would cost every other cluster its range over "+
			"a collision that touches two of them", lease.Block)
	}
}

func TestARangeOverlappingAnOutstandingLeaseIsRefusedAndNamesTheHolder(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, _ string) {
		alpha := mustAllocate(t, a, "alpha")

		err := a.Check(alpha.Block.Masked())
		var collide *LeaseCollisionError
		if !errors.As(err, &collide) {
			t.Fatalf("Check(%s) = %v, want a refusal: cluster alpha holds it", alpha.Block, err)
		}
		if !strings.Contains(err.Error(), "alpha") {
			t.Fatalf("the refusal for a range that is already leased does not name the cluster "+
				"holding it, so nothing reading it can tell which cluster is in the way: %v", err)
		}
		if collide.Held.Cluster != "alpha" {
			t.Fatalf("the refusal names holder %q, want alpha", collide.Held.Cluster)
		}

		beta := mustAllocate(t, a, "beta")
		if beta.Block.Overlaps(alpha.Block) {
			t.Fatalf("alpha holds %s and beta was handed %s, which overlaps it",
				alpha.Block, beta.Block)
		}
	})
}

func TestTheSameClusterAskingTwiceGetsTheSameRangeAndNotASecondOne(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, path string) {
		first := mustAllocate(t, a, "alpha")
		second := mustAllocate(t, a, "alpha")
		if first.Block != second.Block {
			t.Fatalf("cluster alpha first held %s and was handed %s on its second request. A second "+
				"range for one cluster strands the first: the nodes addressed in it keep their "+
				"addresses while the pool believes the range is free",
				first.Block, second.Block)
		}
		if got := len(a.Leases()); got != 1 {
			t.Fatalf("after two requests from one cluster the record holds %d leases, want 1", got)
		}
		restarted := reopenWith(t, path, DefaultPool())
		third := mustAllocate(t, restarted, "alpha")
		if third.Block != first.Block {
			t.Fatalf("after a restart cluster alpha holds %s, but its range before the restart was "+
				"%s. The cluster's nodes are addressed in the first range, so moving it is renumbering "+
				"a running cluster", third.Block, first.Block)
		}
		if got := len(restarted.Leases()); got != 1 {
			t.Fatalf("after the restart the record holds %d leases, want 1: a restart that leases a "+
				"second range for every cluster it already holds one for grows the record on every "+
				"start", got)
		}
	})
}

func TestARestartReFindsTheRangeItsClusterAlreadyHolds(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, path string) {
		alpha := mustAllocate(t, a, "alpha")
		beta := mustAllocate(t, a, "beta")

		restarted := reopenWith(t, path, DefaultPool())

		for _, want := range []Lease{alpha, beta} {
			got, ok := restarted.LeaseFor(want.Cluster)
			if !ok {
				t.Fatalf("after the restart cluster %s holds no range, though the lease file records "+
					"one", want.Cluster)
			}
			if got.Block != want.Block {
				t.Fatalf("after the restart cluster %s holds %s, want %s", want.Cluster, got.Block, want.Block)
			}
			if !got.AllocatedAt.Equal(want.AllocatedAt) {
				t.Fatalf("after the restart cluster %s's lease is dated %s, want %s: a restart "+
					"rewrites the record rather than adopting it", want.Cluster, got.AllocatedAt, want.AllocatedAt)
			}
			if got.Gateway() != want.Gateway() {
				t.Fatalf("after the restart cluster %s's gateway is %s, want %s, so a guest's "+
					"default route moved under it", want.Cluster, got.Gateway(), want.Gateway())
			}
		}
		if got := len(restarted.Leases()); got != 2 {
			t.Fatalf("after the restart the record holds %d leases, want 2", got)
		}
		if gamma := mustAllocate(t, restarted, "gamma"); gamma.Block == alpha.Block || gamma.Block == beta.Block {
			t.Fatalf("a cluster leased after the restart was handed %s, a range a cluster already "+
				"recorded in the file holds", gamma.Block)
		}
	})
}

func TestExhaustionIsRefusedRatherThanWrapped(t *testing.T) {
	withAllocator(t, tinyPool(), func(a *Allocator, _ string) {
		for i := 0; i < 8; i++ {
			mustAllocate(t, a, string(rune('a'+i)))
		}
		if got := len(a.Leases()); got != 8 {
			t.Fatalf("a /29 holds 8 ranges of /29 and %d were leased", got)
		}

		_, err := a.Allocate(Request{Cluster: "ninth"})
		var exhausted *PoolExhaustedError
		if !errors.As(err, &exhausted) {
			t.Fatalf("the ninth cluster got %v, want a refusal: every range in the pool is leased",
				err)
		}
		if exhausted.Candidates != 8 {
			t.Fatalf("the refusal counts %d candidates, want 8 -- the pool is %s",
				exhausted.Candidates, tinyPool())
		}
		if exhausted.LeaseSkips != 8 {
			t.Fatalf("the refusal says %d ranges were refused for overlapping another cluster's "+
				"lease, want 8", exhausted.LeaseSkips)
		}
		if !strings.Contains(err.Error(), "10.200.0.0/26") {
			t.Fatalf("the exhaustion refusal does not name the pool: %v", err)
		}
		if !strings.Contains(err.Error(), "exhausted") {
			t.Fatalf("the exhaustion refusal does not say the pool is exhausted: %v", err)
		}
		first := a.Leases()[0]
		if !strings.Contains(err.Error(), first.Block.String()) || !strings.Contains(err.Error(), first.Cluster) {
			t.Fatalf("the refusal does not name %s, held by cluster %s, so nothing reading it can "+
				"see which lease to free: %v", first.Block, first.Cluster, err)
		}
	})
}

func TestExhaustionSaysHowManyCandidatesTheHostCostAndHowManyTheLeasesCost(t *testing.T) {
	pool := Pool{Base: netip.MustParsePrefix("10.100.0.0/26"), Bits: 29}
	host := []HostNetwork{
		{Interface: "docker0", Address: netip.MustParseAddr("10.100.0.1"), Prefix: netip.MustParsePrefix("10.100.0.0/29")},
		{Interface: "br-1a2b", Address: netip.MustParseAddr("10.100.0.9"), Prefix: netip.MustParsePrefix("10.100.0.8/29")},
		{Interface: "br-3c4d", Address: netip.MustParseAddr("10.100.0.17"), Prefix: netip.MustParsePrefix("10.100.0.16/29")},
		{Interface: "br-5e6f", Address: netip.MustParseAddr("10.100.0.25"), Prefix: netip.MustParsePrefix("10.100.0.24/29")},
		{Interface: "br-7a8b", Address: netip.MustParseAddr("10.100.0.33"), Prefix: netip.MustParsePrefix("10.100.0.32/29")},
		{Interface: "br-9c0d", Address: netip.MustParseAddr("10.100.0.41"), Prefix: netip.MustParsePrefix("10.100.0.40/29")},
	}
	dir := t.TempDir()
	a, err := openWithPool(Config{
		LeasePath:    filepath.Join(dir, "networks.json"),
		HostNetworks: func() ([]HostNetwork, error) { return host, nil },
	}, pool)
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	mustAllocate(t, a, "alpha")
	mustAllocate(t, a, "beta")

	_, err = a.Allocate(Request{Cluster: "gamma"})
	var exhausted *PoolExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("Allocate = %v, want an exhaustion refusal", err)
	}
	if exhausted.HostSkips != 6 || exhausted.LeaseSkips != 2 {
		t.Fatalf("the refusal reports %d host skips and %d lease skips, want 6 and 2. The pool holds "+
			"8 ranges, six of them are networks this host carries and two are leased, and the count "+
			"is how an operator tells which of the two costs to attack",
			exhausted.HostSkips, exhausted.LeaseSkips)
	}
	if !strings.Contains(err.Error(), "docker0") {
		t.Fatalf("the refusal does not name the interface that cost the pool a range: %v", err)
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "10.100.0.48/29") {
		t.Fatalf("the refusal does not name the first lease that refused a candidate, cluster alpha "+
			"in 10.100.0.48/29: %v", err)
	}
	if !strings.Contains(err.Error(), "every one of its 8 ranges was walked") {
		t.Fatalf("the refusal does not say how many candidates it walked: %v", err)
	}
}

func TestAPoolEntirelyCoveredByTheHostIsExhaustedAndNotRefusedAtOpen(t *testing.T) {
	pool := Pool{Base: netip.MustParsePrefix("10.100.0.0/29"), Bits: 29}
	host := []HostNetwork{
		{Interface: "enp8s0", Address: netip.MustParseAddr("10.100.0.1"), Prefix: netip.MustParsePrefix("10.100.0.0/29")},
	}
	dir := t.TempDir()
	a, err := openWithPool(Config{
		LeasePath:    filepath.Join(dir, "networks.json"),
		HostNetworks: func() ([]HostNetwork, error) { return host, nil },
	}, pool)
	if err != nil {
		t.Fatalf("openWithPool: %v -- a host network over the whole pool makes every candidate "+
			"conflicting, which is exhaustion at allocation time and not a reason to refuse to "+
			"start", err)
	}
	_, err = a.Allocate(Request{Cluster: "alpha"})
	var exhausted *PoolExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("Allocate = %v, want an exhaustion refusal", err)
	}
	if exhausted.HostSkips != 1 || exhausted.LeaseSkips != 0 {
		t.Fatalf("the refusal reports %d host skips and %d lease skips, want 1 and 0",
			exhausted.HostSkips, exhausted.LeaseSkips)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "networks.json")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused allocation wrote a lease file: a refusal that records something is "+
			"not a refusal (stat err %v)", statErr)
	}
}

func TestARangeOfTheWrongWidthOrOutsideThePoolIsNotALease(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, _ string) {
		for _, block := range []string{"10.100.0.0/28", "10.101.0.0/24", "10.100.0.0/29", "10.100.0.0/25"} {
			err := a.Check(netip.MustParsePrefix(block))
			if err == nil {
				t.Fatalf("Check(%s) accepted a range that is not a block of the pool %s", block, a.Pool())
			}
			var out *OutOfPoolError
			if !errors.As(err, &out) {
				t.Fatalf("Check(%s) = %v, want a refusal for a range that is not a block of the pool",
					block, err)
			}
		}
		if err := a.Check(netip.MustParsePrefix("10.100.0.0/24")); err != nil {
			t.Fatalf("Check(10.100.0.0/24) = %v, want nil: it is the pool's first range", err)
		}
	})
}

func TestTheGatewaysAndGuestAddressesComeOutOfTheLeasedRange(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, _ string) {
		lease := mustAllocate(t, a, "alpha")

		if got, want := lease.Gateway(), netip.MustParseAddr("10.100.0.1"); got != want {
			t.Fatalf("the gateway of %s is %s, want %s. pasta's -a and -g take this value, so it "+
				"must be the range's own first usable address and not a constant: a constant here is "+
				"one host's router repeated as every cluster's endpoint", lease.Block, got, want)
		}
		if got, want := lease.Netmask(), netip.MustParseAddr("255.255.255.0"); got != want {
			t.Fatalf("the netmask of %s is %s, want %s -- pasta's -n takes this value",
				lease.Block, got, want)
		}
		if got := lease.PrefixBits(); got != 24 {
			t.Fatalf("the range's prefix is /%d, want /24", got)
		}
		if got := lease.HostCount(); got != 254 {
			t.Fatalf("the range holds %d usable addresses, want 254: a /24 has 256 addresses and "+
				"the network and broadcast addresses are not among them", got)
		}
		first, err := lease.Host(1)
		if err != nil {
			t.Fatalf("Host(1): %v", err)
		}
		if first != lease.Gateway() {
			t.Fatalf("Host(1) is %s and the gateway is %s: the first usable address is the gateway "+
				"pasta hands the guest as its default route", first, lease.Gateway())
		}
		for i := 2; i <= 10; i++ {
			got, err := lease.Host(i)
			if err != nil {
				t.Fatalf("Host(%d): %v", i, err)
			}
			if !lease.Contains(got) {
				t.Fatalf("Host(%d) is %s, which is outside the range %s it was taken from",
					i, got, lease.Block)
			}
		}
		last, err := lease.Host(254)
		if err != nil {
			t.Fatalf("Host(254): %v", err)
		}
		if want := netip.MustParseAddr("10.100.0.254"); last != want {
			t.Fatalf("the last guest address is %s, want %s", last, want)
		}
		for _, n := range []int{0, 255, 4096, -1} {
			if _, err := lease.Host(n); err == nil {
				t.Fatalf("Host(%d) was accepted, but the range %s holds 254 addresses numbered 1 "+
					"to 254", n, lease.Block)
			}
		}
	})
}

func TestEachClusterRangeCarriesItsOwnGatewayAndNoHostAddress(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, _ string) {
		alpha := mustAllocate(t, a, "alpha")
		beta := mustAllocate(t, a, "beta")
		if alpha.Gateway() == beta.Gateway() {
			t.Fatalf("two clusters share the gateway %s, so both clusters' guests are handed the "+
				"same default route", alpha.Gateway())
		}
		for _, l := range []Lease{alpha, beta} {
			for _, host := range thisHost {
				if l.Contains(host.Address) {
					t.Fatalf("cluster %s's range %s contains %s, an address this host carries on %s",
						l.Cluster, l.Block, host.Address, host.Interface)
				}
			}
		}
	})
}

func TestTheLeaseFileIsWrittenWhereARestartCanFindItAndNothingElseCanReadIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leases", "networks.json")
	a, err := openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, DefaultPool())
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	func() {
		lease := mustAllocate(t, a, "alpha")

		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := st.Mode().Perm(); got != 0o600 {
			t.Fatalf("the lease file is mode %o, want 600: every other process on this host that "+
				"can read it can lease a range of its own from a pool it is not part of", got)
		}
		dir, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("stat %s: %v", filepath.Dir(path), err)
		}
		if got := dir.Mode().Perm(); got != 0o700 {
			t.Fatalf("the lease directory is mode %o, want 700", got)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var parsed leaseFile
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("the lease file is not JSON: %v", err)
		}
		if parsed.Pool != "10.100.0.0/16" {
			t.Fatalf("the lease file records pool %q, want 10.100.0.0/16: the pool a lease came from "+
				"is part of the record, because a build leasing from another pool cannot read these "+
				"ranges", parsed.Pool)
		}
		if len(parsed.Leases) != 1 || parsed.Leases[0].Block != lease.Block.Masked().String() {
			t.Fatalf("the lease file records %+v, want one lease for %s", parsed.Leases, lease.Block)
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("the atomic write left %s.tmp behind (stat err %v)", path, err)
		}
	}()
}

func TestAnUnreadableLeaseFileIsRefusedRatherThanStartedOver(t *testing.T) {
	pool := DefaultPool()
	dir := t.TempDir()
	path := filepath.Join(dir, "networks.json")
	a, err := openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, pool)
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	mustAllocate(t, a, "alpha")

	if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, pool)
	if err == nil {
		t.Fatalf("a lease file that cannot be parsed was accepted, so the gateway started with an " +
			"empty record: it would lease a second range to cluster alpha and strand the first")
	}
	if !strings.Contains(err.Error(), "refusing to start with an empty store over it") {
		t.Fatalf("the refusal for an unreadable lease file does not say it is refusing rather than "+
			"starting empty: %v", err)
	}
}

func TestALeaseFileWrittenByAnotherPoolIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "networks.json")
	a, err := openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, tinyPool())
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	mustAllocate(t, a, "alpha")

	_, err = openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, DefaultPool())
	if err == nil {
		t.Fatalf("a lease file written from pool %s was read by a build leasing from %s. The two "+
			"are different spaces: the ranges in the file are not blocks of the new pool, so the "+
			"walk would not see them and would hand a second cluster a range a live cluster's nodes "+
			"are addressed in", tinyPool(), DefaultPool())
	}
	if !strings.Contains(err.Error(), "10.200.0.0/26") {
		t.Fatalf("the refusal does not name the pool the leases came from: %v", err)
	}
}

func TestALeaseFileNamingOneClusterTwiceIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "networks.json")
	body := `{"version":1,"pool":"10.100.0.0/16","leases":[
	 {"cluster":"alpha","block":"10.100.0.0/24"},
	 {"cluster":"alpha","block":"10.100.1.0/24"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, DefaultPool())
	if err == nil {
		t.Fatalf("a lease file naming one cluster twice was accepted. One of the two ranges is " +
			"held by nobody and the walk hands it to the next cluster, whose nodes are then " +
			"addressed in a range this cluster's nodes may still be in")
	}
	if !strings.Contains(err.Error(), "names cluster alpha twice") {
		t.Fatalf("the refusal does not say what is wrong with the file: %v", err)
	}
}

func TestALeaseFileWithOneRangeHeldByTwoClustersIsRefusedAtEveryAllocation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "networks.json")
	body := `{"version":1,"pool":"10.100.0.0/16","leases":[
	 {"cluster":"alpha","block":"10.100.0.0/24"},
	 {"cluster":"beta","block":"10.100.0.0/24"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := openWithPool(Config{LeasePath: path, HostNetworks: fakeHost}, DefaultPool())
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	_, err = a.Allocate(Request{Cluster: "gamma"})
	if err == nil {
		t.Fatalf("an allocation succeeded over a record holding one range for two clusters")
	}
	if !strings.Contains(err.Error(), "10.100.0.0/24") || !strings.Contains(err.Error(), "alpha") ||
		!strings.Contains(err.Error(), "beta") {
		t.Fatalf("the refusal does not name the range and both holders: %v", err)
	}
	if !strings.Contains(err.Error(), "Fix the duplicate LEASE") {
		t.Fatalf("the refusal does not point at the duplicate lease rather than the clusters: %v", err)
	}
}

func TestAClusterWithNoNameIsRefused(t *testing.T) {
	withAllocator(t, DefaultPool(), func(a *Allocator, path string) {
		if _, err := a.Allocate(Request{}); err == nil {
			t.Fatalf("an unnamed cluster was leased a range. The lease is looked up by cluster name " +
				"on every later start, so an unnamed one can never be found again and the next start " +
				"of the same cluster is handed a second range")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a refused allocation wrote a lease file (stat err %v)", err)
		}
	})
}

func TestAHostWhoseNetworksCannotBeReadIsRefusedRatherThanLeasedBlind(t *testing.T) {
	boom := errors.New("netlink said no")
	dir := t.TempDir()
	path := filepath.Join(dir, "networks.json")
	_, err := openWithPool(Config{
		LeasePath:    path,
		HostNetworks: func() ([]HostNetwork, error) { return nil, boom },
	}, DefaultPool())
	if err == nil {
		t.Fatalf("a gateway started on a host whose networks could not be enumerated. Every range " +
			"it leases is non-conflicting only because it was compared against those networks, so " +
			"it would lease a range nobody checked and report success")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("the refusal does not carry the enumeration's own error: %v", err)
	}
	for _, want := range []string{"before this gateway leases anything", "compared against the host's own networks"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not explain why an unreadable host stops the gateway (%q "+
				"missing): %v", want, err)
		}
	}
}

func TestHostNetworksReadsThisMachinesOwnInterfaces(t *testing.T) {
	got, err := HostNetworks()
	if err != nil {
		t.Fatalf("HostNetworks: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("this host reports no networks at all, not even loopback")
	}
	loopback := netip.MustParseAddr("127.0.0.1")
	found := false
	for _, h := range got {
		if h.Prefix != h.Prefix.Masked() {
			t.Fatalf("%s is not the masked form of itself, so the network an interface sits in is "+
				"not what is being compared against a candidate range", h)
		}
		if h.Prefix.Bits() > h.Address.BitLen() {
			t.Fatalf("%s carries a %d-bit mask, which is longer than its own address", h, h.Prefix.Bits())
		}
		if h.Interface == "" {
			t.Fatalf("%s names no interface, so a collision with it cannot say what to move", h)
		}
		if h.Prefix.Contains(loopback) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no network this host reports contains 127.0.0.1, so loopback is not being "+
			"enumerated and a range of 127.0.0.0/8 could be leased: %v", got)
	}
}

func TestThePoolIsAConstantOfThisPackage(t *testing.T) {
	pool := DefaultPool()
	if got := pool.Masked().String(); got != "10.100.0.0/16" {
		t.Fatalf("the pool is %s, want 10.100.0.0/16", got)
	}
	if got := pool.Bits; got != 24 {
		t.Fatalf("the pool is cut into /%d ranges, want /24", got)
	}
	if got := pool.Blocks(); got != 256 {
		t.Fatalf("the pool holds %d ranges, want 256: a /16 cut into /24s, with no partial range left "+
			"at either edge because 24 is a whole number of bits deeper than 16", got)
	}
	if got, want := pool.BlockAt(0), netip.MustParsePrefix("10.100.0.0/24"); got != want {
		t.Fatalf("the pool's first range is %s, want %s", got, want)
	}
	if got, want := pool.BlockAt(255), netip.MustParsePrefix("10.100.255.0/24"); got != want {
		t.Fatalf("the pool's last range is %s, want %s", got, want)
	}
	if pool.Contains(netip.MustParsePrefix("10.101.0.0/24")) {
		t.Fatalf("the pool claims to contain 10.101.0.0/24, which is outside it")
	}
}

func TestConcurrentClustersAreNeverHandedOneRange(t *testing.T) {
	pool := Pool{Base: netip.MustParsePrefix("10.100.0.0/24"), Bits: 29}
	dir := t.TempDir()
	a, err := openWithPool(Config{
		LeasePath:    filepath.Join(dir, "networks.json"),
		HostNetworks: fakeHost,
	}, pool)
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	var mu sync.Mutex
	seen := map[netip.Prefix]string{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		cluster := string(rune('a' + i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := a.Allocate(Request{Cluster: cluster})
			if err != nil {
				t.Errorf("Allocate(%s): %v", cluster, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if other, ok := seen[lease.Block]; ok {
				t.Errorf("clusters %s and %s were both handed %s", other, cluster, lease.Block)
				return
			}
			seen[lease.Block] = cluster
		}()
	}
	wg.Wait()
	if len(seen) != 8 {
		t.Fatalf("8 clusters were handed %d distinct ranges", len(seen))
	}
}

func TestALeaseIsRecordedWithTheTimeItWasGranted(t *testing.T) {
	fixed := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "networks.json")
	a, err := openWithPool(Config{
		LeasePath:    path,
		HostNetworks: fakeHost,
		Now:          func() time.Time { return fixed },
	}, DefaultPool())
	if err != nil {
		t.Fatalf("openWithPool: %v", err)
	}
	lease := mustAllocate(t, a, "alpha")
	if !lease.AllocatedAt.Equal(fixed) {
		t.Fatalf("the lease is dated %s, want %s", lease.AllocatedAt, fixed)
	}
	restarted := reopenWith(t, path, DefaultPool())
	got, ok := restarted.LeaseFor("alpha")
	if !ok {
		t.Fatalf("after the restart cluster alpha holds no range")
	}
	if !got.AllocatedAt.Equal(fixed) {
		t.Fatalf("after the restart the lease is dated %s, want %s", got.AllocatedAt, fixed)
	}
}

func TestReleaseFreesTheRangeForTheNextCluster(t *testing.T) {
	withAllocator(t, tinyPool(), func(a *Allocator, path string) {
		alpha := mustAllocate(t, a, "alpha")
		if err := a.Release("alpha"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, ok := a.LeaseFor("alpha"); ok {
			t.Fatalf("cluster alpha still holds its range after Release")
		}
		restarted := reopenWith(t, path, tinyPool())
		if _, ok := restarted.LeaseFor("alpha"); ok {
			t.Fatalf("the release was not persisted, so a restart resurrects the lease")
		}
		next := mustAllocate(t, restarted, "beta")
		if next.Block != alpha.Block {
			t.Fatalf("after releasing %s the next cluster was handed %s, so the released range is "+
				"lost rather than free", alpha.Block, next.Block)
		}
	})
}
