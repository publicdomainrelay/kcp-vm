package kcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

const (
	NoExpiryNamespace    = "default"
	NoExpiryLabel        = "socialweb.computer/kubeconfig-no-expiry"
	NoExpiryFieldManager = "socialweb-gateway-no-expiry"
)

func NoExpiryRecordName(serial string) string {
	return "noexpiry-" + strings.ToLower(strings.ReplaceAll(serial, ":", ""))
}

func (c *Client) RecordNoExpiry(ctx context.Context, did, cluster, serial string, issuedAt time.Time) error {
	home := HomePath(did, cluster)
	core, err := c.coreResource(home)
	if err != nil {
		return err
	}
	ns := corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: NoExpiryNamespace}}
	if err := applyObject(ctx, core.Patch(types.ApplyPatchType).Resource("namespaces").Name(NoExpiryNamespace), ns); err != nil {
		return fmt.Errorf("kcpclient: the namespace for no-expiry records in %s: %w", home, err)
	}
	name := NoExpiryRecordName(serial)
	record := corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: NoExpiryNamespace, Labels: map[string]string{NoExpiryLabel: "true"}},
		Data:       map[string]string{"serial": serial, "did": did, "cluster": cluster, "issuedAt": issuedAt.UTC().Format(time.RFC3339)}}
	if err := applyObject(ctx, core.Patch(types.ApplyPatchType).Namespace(NoExpiryNamespace).Resource("configmaps").Name(name), record); err != nil {
		return fmt.Errorf("kcpclient: the no-expiry record in %s: %w", home, err)
	}
	return nil
}

func applyObject(ctx context.Context, req *rest.Request, obj any) error {
	body, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return req.Param("fieldManager", NoExpiryFieldManager).Param("force", "true").Body(body).Do(ctx).Error()
}
