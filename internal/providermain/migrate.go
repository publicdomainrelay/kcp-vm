package providermain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/subnetallocator"
)

type legacyKeys struct {
	StateDir string
	Ports    *portallocator.Allocator
	Networks *subnetallocator.Allocator
	Log      *slog.Logger
}

func migrateLegacyKeys(ctx context.Context, registry clusterprovider.Clusters, d legacyKeys) error {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	refs, err := registry.Refs(ctx)
	if err != nil {
		return fmt.Errorf(
			"clusterprovider: listing the Cluster CRs before the legacy-key migration: %w. The "+
				"migration renames a state dir, a node dir and a mint credential from the bare "+
				"cluster name to the cluster's own key, and a list that failed is not an empty "+
				"tree: it would leave every legacy directory unclaimed and every later pass "+
				"reading an empty state dir over a cluster that is running", err)
	}

	byName := map[string][]clusterprovider.Ref{}
	for _, ref := range refs {
		byName[ref.Name] = append(byName[ref.Name], ref)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		group := byName[name]
		if len(group) != 1 {
			homes := make([]string, 0, len(group))
			for _, ref := range group {
				homes = append(homes, ref.Home)
			}
			d.Log.Warn("cluster provider: two or more home workspaces hold a Cluster with this "+
				"name, so the legacy directories named by it belong to no one cluster and are left "+
				"where they are",
				"cluster", name, "homes", homes)
			continue
		}
		key, err := group[0].Key()
		if err != nil {
			return err
		}
		if err := migrateOne(d, name, key); err != nil {
			return err
		}
	}
	return nil
}

func migrateOne(d legacyKeys, name, key string) error {
	if name == key {
		return nil
	}
	instance, err := clusterprovider.InstanceName(name)
	if err != nil {
		return err
	}
	instanceKey, err := clusterprovider.InstanceName(key)
	if err != nil {
		return err
	}
	for _, move := range []struct{ parent, from, to string }{
		{d.StateDir, instance, instanceKey},
		{d.StateDir, name, key},
	} {
		if move.parent == "" || move.from == move.to {
			continue
		}
		if err := renameLegacy(d.Log, move.parent, move.from, move.to, name, key); err != nil {
			return err
		}
	}
	if d.Ports != nil {
		moved, err := d.Ports.Rekey(name, key)
		if err != nil {
			return fmt.Errorf("clusterprovider: re-keying the port leases of %s to %s: %w", name, key, err)
		}
		if moved > 0 {
			d.Log.Info("cluster provider: re-keyed the port leases of a legacy cluster name",
				"cluster", name, "key", key, "leases", moved)
		}
	}
	if d.Networks != nil {
		moved, err := d.Networks.Rekey(name, key)
		if err != nil {
			return fmt.Errorf("clusterprovider: re-keying the subnet lease of %s to %s: %w", name, key, err)
		}
		if moved > 0 {
			d.Log.Info("cluster provider: re-keyed the subnet lease of a legacy cluster name",
				"cluster", name, "key", key, "leases", moved)
		}
	}
	return nil
}

func renameLegacy(log *slog.Logger, parent, from, to, name, key string) error {
	fromPath := filepath.Join(parent, from)
	toPath := filepath.Join(parent, to)
	if _, err := os.Stat(fromPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("clusterprovider: reading %s before the legacy-key migration: %w", fromPath, err)
	}
	if _, err := os.Stat(toPath); err == nil {
		log.Warn("cluster provider: a legacy directory and this cluster's keyed directory both "+
			"exist, so the legacy one is left exactly where it is and nothing is merged into the "+
			"keyed one",
			"cluster", name, "key", key, "legacy", fromPath, "keyed", toPath)
		return nil
	}
	if err := os.Rename(fromPath, toPath); err != nil {
		return fmt.Errorf("clusterprovider: renaming %s to %s: %w. The cluster's identity inside "+
			"this provider is the key, and a state dir still named by the bare cluster name is one "+
			"no later pass finds: the node records, the instance token and the identity bundle "+
			"would all read as absent, and the first pass would provision over a cluster that is "+
			"already running", fromPath, toPath, err)
	}
	log.Info("cluster provider: migrated a directory named by a bare cluster name to this "+
		"cluster's key", "cluster", name, "key", key, "from", fromPath, "to", toPath)
	return nil
}
