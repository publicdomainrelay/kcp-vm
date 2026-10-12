package providermain

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
)

func Startup(ctx context.Context, registry clusterprovider.Clusters, units *clusterprovider.UnitsHost, log *slog.Logger) error {
	refs, err := registry.Refs(ctx)
	if err != nil {
		return fmt.Errorf("clusterprovider: listing the Cluster CRs before adoption: %w", err)
	}
	for _, ref := range refs {
		cl, err := registry.Read(ctx, ref)
		if err != nil {
			return fmt.Errorf("clusterprovider: reading the Cluster %s before adoption: %w", ref.Name, err)
		}
		key, err := ref.Key()
		if err != nil {
			return err
		}
		members, err := units.Adoption(ctx, key)
		if err != nil {
			return fmt.Errorf("clusterprovider: listing the member units of %s: %w", key, err)
		}
		if !materialiser.StartupOwns(*cl) {
			log.Info("cluster provider: adoption", "cluster", ref.Name, "key", key, "members", members,
				"decision", "not running here; the next pass owns it")
			continue
		}
		if _, err := units.EnsureStore(ctx, key); err != nil {
			return fmt.Errorf("clusterprovider: the Postgres object of %s before adoption: %w", key, err)
		}
		decision := "adopted: every member keeps running; the passes verify and re-place only what is missing or restarted"
		if len(members) == 0 {
			decision = "no member unit exists: the first pass starts the cluster in order"
		}
		log.Info("cluster provider: adoption", "cluster", ref.Name, "key", key, "slice", units.Slice(key),
			"store", units.StoreUnit(key), "members", members, "decision", decision)
	}
	return nil
}
