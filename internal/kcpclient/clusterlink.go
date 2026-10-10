package kcpclient

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

func (c *Client) ReadClusterLink(ctx context.Context, logicalCluster, name string) (*v1alpha1.ClusterLink, error) {
	r, err := c.resource(logicalCluster, socialwebResource)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, errors.New("kcpclient: ReadClusterLink needs a link name")
	}
	var link v1alpha1.ClusterLink
	err = r.Get().Resource("clusterlinks").Name(name).Do(ctx).Into(&link)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kcpclient: read clusterlink %s: %w", name, err)
	}
	return &link, nil
}
