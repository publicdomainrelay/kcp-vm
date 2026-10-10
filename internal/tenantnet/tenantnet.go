package tenantnet

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

//go:embed manifests/*.yaml
var manifests embed.FS

const (
	Address = "169.254.20.10"

	NodeLabel = "socialweb.computer/node-local-dns"

	DNSName = "socialweb-node-dns"

	NodeReaderName = "socialweb-meshd-node-reader"
)

var ManifestOrder = []string{
	"manifests/node-reader-rbac.yaml",
	"manifests/node-local-dns.yaml",
}

var ErrUnknownKind = errors.New("tenantnet: a manifest names a kind this package does not apply")

var resources = map[string]schema.GroupVersionResource{
	"ServiceAccount":     {Version: "v1", Resource: "serviceaccounts"},
	"ConfigMap":          {Version: "v1", Resource: "configmaps"},
	"ClusterRole":        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	"ClusterRoleBinding": {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
	"DaemonSet":          {Group: "apps", Version: "v1", Resource: "daemonsets"},
}

func Objects() ([]unstructured.Unstructured, error) {
	var out []unstructured.Unstructured
	for _, name := range ManifestOrder {
		raw, err := manifests.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("tenantnet: reading %s: %w", name, err)
		}
		dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
		for {
			var obj map[string]any
			if err := dec.Decode(&obj); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, fmt.Errorf("tenantnet: decoding %s: %w", name, err)
			}
			if len(obj) == 0 {
				continue
			}
			u := unstructured.Unstructured{Object: obj}
			if _, ok := resources[u.GetKind()]; !ok {
				return nil, fmt.Errorf("%w: %s %s in %s", ErrUnknownKind, u.GetKind(), u.GetName(), name)
			}
			out = append(out, u)
		}
	}
	return out, nil
}

func Apply(ctx context.Context, dyn dynamic.Interface, manager string) error {
	objs, err := Objects()
	if err != nil {
		return err
	}
	force := true
	for i := range objs {
		u := &objs[i]
		gvr := resources[u.GetKind()]
		body, err := json.Marshal(u.Object)
		if err != nil {
			return fmt.Errorf("tenantnet: marshalling the %s %s: %w", u.GetKind(), u.GetName(), err)
		}
		var ri dynamic.ResourceInterface = dyn.Resource(gvr)
		if ns := u.GetNamespace(); ns != "" {
			ri = dyn.Resource(gvr).Namespace(ns)
		}
		if _, err := ri.Patch(ctx, u.GetName(), types.ApplyPatchType, body,
			metav1.PatchOptions{FieldManager: manager, Force: &force}); err != nil {
			return fmt.Errorf("tenantnet: applying the %s %s: %w", u.GetKind(), u.GetName(), err)
		}
	}
	return nil
}
