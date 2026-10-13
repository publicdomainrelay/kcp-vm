package denoruntime

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/provider"
)

var openBaoServerGVR = schema.GroupVersionResource{Group: v1alpha1.GroupName, Version: v1alpha1.Version, Resource: "openbaoservers"}

type openBaoServerSources struct {
	cfg *rest.Config
}

var _ provider.ProjectedSources = openBaoServerSources{}

func (s openBaoServerSources) OpenBaoClient(ctx context.Context, workspace, server, client string) (provider.OpenBaoClientFiles, error) {
	cfg := rest.CopyConfig(s.cfg)
	host, _, _ := strings.Cut(strings.TrimSuffix(cfg.Host, "/"), "/clusters/")
	cfg.Host = host + "/clusters/" + workspace
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return provider.OpenBaoClientFiles{}, err
	}
	u, err := dyn.Resource(openBaoServerGVR).Get(ctx, server, metav1.GetOptions{})
	if err != nil {
		return provider.OpenBaoClientFiles{}, fmt.Errorf("the OpenBaoServer %s in %s: %w", server, workspace, err)
	}
	var o v1alpha1.OpenBaoServer
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &o); err != nil {
		return provider.OpenBaoClientFiles{}, err
	}
	if o.Status.Refs == nil || o.Status.Refs.CA == "" {
		return provider.OpenBaoClientFiles{}, fmt.Errorf("the OpenBaoServer %s reports no CA yet (phase %s)", server, o.Status.Phase)
	}
	for _, c := range o.Status.Clients {
		if c.Name == client && c.ClientCert != "" && c.ClientKey != "" {
			return provider.OpenBaoClientFiles{CA: fileRef(o.Status.Refs.CA), Cert: fileRef(c.ClientCert), Key: fileRef(c.ClientKey)}, nil
		}
	}
	return provider.OpenBaoClientFiles{}, fmt.Errorf("the OpenBaoServer %s reports no client %s yet (phase %s)", server, client, o.Status.Phase)
}

func fileRef(ref string) string { return strings.TrimPrefix(ref, "file:") }
