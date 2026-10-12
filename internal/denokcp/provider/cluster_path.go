package provider

import (
	"context"
)

func (r *Registry) ClusterPath(ctx context.Context, id string) (string, error) {
	return r.store.ClusterPath(ctx, id)
}
