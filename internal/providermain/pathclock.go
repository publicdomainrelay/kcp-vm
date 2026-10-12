package providermain

import (
	"context"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
)

const PathOutageMin = 30 * time.Second

type pathClock struct {
	observed  bool
	up        bool
	coldStart bool
	downSince time.Time
	upSince   time.Time
}

func (c *pathClock) observe(up bool, now time.Time) {
	switch {
	case !up && (c.up || !c.observed):
		c.downSince = now
		c.coldStart = !c.observed
	case up && c.observed && !c.up:
		if c.coldStart || now.Sub(c.downSince) >= PathOutageMin {
			c.upSince = now
		}
		c.downSince = time.Time{}
		c.coldStart = false
	}
	c.up = up
	c.observed = true
}

func (c *pathClock) holding() bool {
	return c.observed && !c.up
}

func (c *pathClock) rebase(since time.Time) time.Time {
	if c.upSince.After(since) {
		return c.upSince
	}
	return since
}

func clusterPathUp(tenant tenantClient, links nodemarket.Links, workspaceOf func(ctx context.Context, cluster string) (string, error)) func(ctx context.Context, cluster string) bool {
	return func(ctx context.Context, cluster string) bool {
		client, err := tenant(ctx, cluster)
		if err != nil {
			return false
		}
		if _, err := client.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx); err != nil {
			return false
		}
		if links == nil {
			return true
		}
		workspace, err := workspaceOf(ctx, cluster)
		if err != nil {
			return false
		}
		for _, name := range []string{nodemarket.ApiserverLinkName(cluster), nodemarket.OpenBaoLinkName(cluster)} {
			link, err := links.ReadClusterLink(ctx, workspace, name)
			if err != nil || link == nil || link.Status.Phase != v1alpha1.ClusterLinkListening {
				return false
			}
		}
		return true
	}
}
