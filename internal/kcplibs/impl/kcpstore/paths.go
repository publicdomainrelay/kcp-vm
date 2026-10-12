package kcpstore

import (
	"context"
	"errors"
	"sync"
)

func definitive(err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrNoPath):
		return true
	case IsNotFound(err):
		return true
	}
	return false
}

type PathCache struct {
	store *Store

	mu sync.Mutex

	byID map[string]string
}

func NewPathCache(store *Store) *PathCache {
	return &PathCache{store: store, byID: map[string]string{}}
}

func (c *PathCache) Lookup(ctx context.Context, logicalCluster string) string {
	c.mu.Lock()
	if path, seen := c.byID[logicalCluster]; seen {
		c.mu.Unlock()
		return path
	}
	c.mu.Unlock()

	path := ""
	if c.store != nil {
		resolved, err := c.store.ClusterPath(ctx, logicalCluster)
		if !definitive(err) {
			return ""
		}
		path = resolved
	}
	c.mu.Lock()
	c.byID[logicalCluster] = path
	c.mu.Unlock()
	return path
}

func (c *PathCache) Forget(logicalCluster string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, logicalCluster)
}

func (c *PathCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byID)
}
