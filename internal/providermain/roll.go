package providermain

import (
	"context"
	"log/slog"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

func newRoller(units *clusterprovider.UnitsHost, stack clusterprovider.StackConfig, registry clusterprovider.Clusters,
	alloc *portallocator.Allocator, purposes []portallocator.Purpose, log *slog.Logger) *clusterprovider.Roller {
	refs := func(ctx context.Context) (map[string]clusterprovider.Ref, error) {
		list, err := registry.Refs(ctx)
		if err != nil {
			return nil, err
		}
		out := map[string]clusterprovider.Ref{}
		for _, r := range list {
			if key, err := r.Key(); err == nil {
				out[key] = r
			}
		}
		return out, nil
	}
	return &clusterprovider.Roller{
		Units: units,
		Stack: stack,
		Clusters: func(ctx context.Context) ([]string, error) {
			m, err := refs(ctx)
			if err != nil {
				return nil, err
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			return keys, nil
		},
		Ready: func(ctx context.Context, cluster string) bool {
			m, err := refs(ctx)
			if err != nil {
				return false
			}
			ref, ok := m[cluster]
			if !ok {
				return false
			}
			cl, err := registry.Read(ctx, ref)
			return err == nil && cl != nil && cl.Status.Phase == v1alpha1.ClusterReady
		},
		Leases: func(cluster string) (materialiser.Leases, error) {
			out := materialiser.Leases{}
			for _, p := range purposes {
				if l, ok := alloc.PortFor(cluster, p); ok {
					out[p] = l
				}
			}
			return out, nil
		},
		Relet: func(cluster string, p portallocator.Purpose) (portallocator.Lease, bool, error) {
			return alloc.Relet(portallocator.Request{Cluster: cluster, Purpose: p})
		},
		Log: log,
	}
}
