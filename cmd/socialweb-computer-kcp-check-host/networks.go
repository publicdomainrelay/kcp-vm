package main

import (
	"fmt"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

const poolWalkLimit = 1 << 16

func poolResult(f facts) Result {
	pool := subnetallocator.DefaultPool()
	if err := pool.Validate(); err != nil {
		return Result{Label: "host.allocator-pool", Kind: Missing,
			Detail: fmt.Sprintf("the pool this project leases cluster ranges out of does not "+
				"validate: %v", err)}
	}
	hosts, err := subnetallocator.HostNetworks()
	if err != nil {
		return Result{
			Label: "host.allocator-pool",
			Kind:  Missing,
			Detail: fmt.Sprintf("this host's own networks cannot be enumerated: %v. Every range "+
				"handed to a cluster is non-conflicting only because it was compared against the "+
				"networks this host carries, and the allocator REFUSES to lease at all when that "+
				"enumeration fails -- so this is not a report about a pool, it is the reason the "+
				"next cluster cannot be provisioned, and it presents to an operator as an allocate "+
				"that refuses for no stated reason on a host whose interfaces look fine", err),
		}
	}
	var cost []string
	interfaces := []string{}
	seen := map[string]struct{}{}
	costly := uint64(0)
	for i := uint64(0); i < pool.Blocks(); i++ {
		candidate := pool.BlockAt(i)
		for _, h := range hosts {
			if !candidate.Overlaps(h.Prefix) {
				continue
			}
			costly++
			if len(cost) < 4 {
				cost = append(cost, candidate.String())
			}
			if _, ok := seen[h.Interface]; !ok {
				seen[h.Interface] = struct{}{}
				interfaces = append(interfaces, h.String())
			}
			break
		}
	}
	carried := "this host carries no networks at all"
	if len(hosts) > 0 {
		described := make([]string, 0, len(hosts))
		for _, h := range hosts {
			described = append(described, fmt.Sprintf("%s on %s as %s", h.Prefix, h.Interface, h.Address))
		}
		carried = "this host carries " + strings.Join(described, ", ")
	}
	if pool.Blocks() > poolWalkLimit {
		return Result{
			Label: "host.allocator-pool",
			Kind:  Holds,
			Detail: fmt.Sprintf("the pool %s, %s. This pool is cut into more ranges than this "+
				"check walks, so whether any of them overlaps one of the host's networks is not "+
				"reported: an interface sharing any part of the pool costs the ranges it covers, and "+
				"the allocator refuses those per candidate at allocate time rather than here. %s",
				pool, firstOr(interfaces, "no interface overlaps the pool"), carried),
		}
	}
	if costly == 0 {
		return Result{
			Label: "host.allocator-pool",
			Kind:  Holds,
			Detail: fmt.Sprintf("the pool %s, and not one of its %d ranges overlaps a network this "+
				"host carries, so every range in it is available to a cluster: %s", pool,
				pool.Blocks(), carried),
		}
	}
	if costly == pool.Blocks() {
		return Result{
			Label: "host.allocator-pool",
			Kind:  Missing,
			Detail: fmt.Sprintf("every one of the pool's %d ranges overlaps a network this host "+
				"carries (%s), so no cluster can be leased a range at all and the first allocation "+
				"walks the whole pool and refuses. %s. This is legal and it is fatal: the allocator "+
				"keeps no record of what the host carries, so an operator sees a pool-exhausted "+
				"refusal and looks for leases to free, when the ranges were never available",
				pool.Blocks(), strings.Join(interfaces, ", "), carried),
		}
	}
	return Result{
		Label: "host.allocator-pool",
		Kind:  Holds,
		Detail: fmt.Sprintf("the pool %s is cut into %d ranges and %d of them overlap a network "+
			"this host carries (%s), for example %s. That is legal and it is not free: every range "+
			"inside one of those networks is refused per candidate at allocate time, so the pool "+
			"holds %d usable ranges and not %d, and an operator should learn that here rather than "+
			"from a refusal while provisioning. %s",
			pool, pool.Blocks(), costly, strings.Join(interfaces, ", "), strings.Join(cost, ", "),
			pool.Blocks()-costly, pool.Blocks(), carried),
	}
}

func firstOr(lines []string, fallback string) string {
	if len(lines) == 0 {
		return fallback
	}
	return strings.Join(lines, ", ")
}
