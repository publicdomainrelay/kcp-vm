package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
)

type postgresRegistry struct {
	mu sync.Mutex
	by map[string]*clusterprovider.Postgres
}

func newPostgresRegistry() *postgresRegistry {
	return &postgresRegistry{by: map[string]*clusterprovider.Postgres{}}
}

func (r *postgresRegistry) set(cluster string, pg *clusterprovider.Postgres) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[cluster] = pg
}

func (r *postgresRegistry) get(cluster string) (*clusterprovider.Postgres, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pg, ok := r.by[cluster]
	return pg, ok && pg != nil
}

func (r *postgresRegistry) up(cluster string) bool {
	r.mu.Lock()
	pg, ok := r.by[cluster]
	r.mu.Unlock()
	if !ok || pg == nil {
		return false
	}
	return pg.Up()
}

func (r *postgresRegistry) stopAll(ctx context.Context) error {
	r.mu.Lock()
	handles := make(map[string]*clusterprovider.Postgres, len(r.by))
	for k, v := range r.by {
		handles[k] = v
	}
	r.by = map[string]*clusterprovider.Postgres{}
	r.mu.Unlock()

	var errs []error
	for cluster, pg := range handles {
		if pg == nil {
			continue
		}
		if err := pg.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping the store for %s: %w", cluster, err))
		}
	}
	return errors.Join(errs...)
}

func (r *postgresRegistry) dsn(cluster string) (string, error) {
	r.mu.Lock()
	pg, ok := r.by[cluster]
	r.mu.Unlock()
	if !ok || pg == nil {
		return "", fmt.Errorf(
			"clusterprovider: no postgres handle for %s in this process, so there is no DSN to "+
				"give k3s. An empty --datastore-endpoint makes k3s start its own sqlite and a "+
				"brand-new cluster with no warning, so this is refused rather than defaulted", cluster)
	}
	return pg.DSN(), nil
}

func (r *postgresRegistry) vaultDSN(cluster string) (string, error) {
	r.mu.Lock()
	pg, ok := r.by[cluster]
	r.mu.Unlock()
	if !ok || pg == nil {
		return "", fmt.Errorf(
			"clusterprovider: no per-cluster postgres handle for %s in this process, so there is "+
				"no store DSN to give the vault. The vault's store IS this cluster's own embedded-"+
				"postgres and it is not a defaulted one, so a vault with no DSN is refused rather "+
				"than started on a store that is not its own", cluster)
	}
	return pg.VaultDSN(), nil
}
