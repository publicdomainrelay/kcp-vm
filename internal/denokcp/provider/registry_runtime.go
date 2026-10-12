package provider

import (
	"context"
	"time"
)

const DefaultServiceAccountNamespace = "default"

func (r *Registry) MintServiceAccountToken(ctx context.Context, logicalCluster, namespace, name string, ttl time.Duration) (string, error) {
	if namespace == "" {
		namespace = DefaultServiceAccountNamespace
	}
	return r.store.MintServiceAccountToken(ctx, logicalCluster, namespace, name, ttl)
}
