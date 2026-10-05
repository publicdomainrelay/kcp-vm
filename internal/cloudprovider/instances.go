package cloudprovider

import "context"

type Instance struct {
	NodeName string `json:"nodeName"`

	Cluster string `json:"cluster"`

	Addresses []string `json:"addresses"`

	InstanceType string `json:"instanceType"`

	Exists bool `json:"exists"`

	Draining bool `json:"draining"`
}

type Instances interface {
	Get(ctx context.Context, nodeName string) (Instance, bool, error)
}
