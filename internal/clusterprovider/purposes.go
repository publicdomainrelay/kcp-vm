package clusterprovider

import (
	"fmt"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

var componentPorts = map[procgroup.Component][]portallocator.Purpose{
	procgroup.ComponentPostgres: {portallocator.PurposePostgres},
	procgroup.ComponentOpenBao:  {portallocator.PurposeOpenBao},
	procgroup.ComponentKine:     {portallocator.PurposeKine},
	procgroup.ComponentK3s: {
		portallocator.PurposeK3sSecure,
		portallocator.PurposeK3sControllerManager,
		portallocator.PurposeK3sScheduler,
		portallocator.PurposeK3sCloudControllerManager,
	},
	procgroup.ComponentCloudControllerManager: nil,
}

var ErrNoPurposeForComponent = fmt.Errorf(
	"clusterprovider: a component of this Stack has no leased port. A component with no lease " +
		"either binds a host-fixed port -- and the first cluster takes it and every later one " +
		"fails at bind -- or it binds nothing, which means it is in the Stack without being run")

func PurposesFor(stack Stack, postgresNeeded bool) ([]portallocator.Purpose, error) {
	if len(stack) == 0 {
		return nil, fmt.Errorf(
			"clusterprovider: an empty Stack leases nothing, and a cluster with no leases is a " +
				"cluster with no ports to hold it to; refusing rather than returning the default carve")
	}
	var out []portallocator.Purpose
	seen := map[portallocator.Purpose]bool{}
	add := func(p portallocator.Purpose) {
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if postgresNeeded {
		add(portallocator.PurposePostgres)
	}
	for _, c := range stack {
		purposes, ok := componentPorts[c]
		if !ok {
			return nil, fmt.Errorf("%w (component %q)", ErrNoPurposeForComponent, c)
		}
		for _, p := range purposes {
			add(p)
		}
	}
	return out, nil
}
