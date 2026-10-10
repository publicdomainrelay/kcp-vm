package tenantclient

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
)

const Namespace = kcpclient.TenantNamespace

const MinTokenTTL = 600 * time.Second

const (
	serviceAccounts  = "serviceaccounts"
	tokenSubresource = "token"

	rowPrefix = "devkey-"

	saPrefix = "did-"
)

var ErrTTLBelowFloor = errors.New("tenantclient: ttl is below the apiserver's 600 s floor")

var ErrNoExpiration = errors.New("tenantclient: the apiserver returned no token expiry")

type Row struct {
	DeviceKey string

	DID string

	Cluster string
}

type Options struct {
	Registry *rest.Config

	RegistryWorkspace string
	RegistryNamespace string

	Audience string

	MintConfig func(did, cluster string) (*rest.Config, error)
}

type Client struct {
	opts   Options
	codecs runtime.NegotiatedSerializer
}

var _ mint.Store = (*Client)(nil)

func New(opts Options) (*Client, error) {
	switch {
	case opts.Registry == nil:
		return nil, errors.New("tenantclient: Registry is required: the binding rows live in kcp")
	case opts.RegistryWorkspace == "":
		return nil, errors.New("tenantclient: RegistryWorkspace is required")
	case opts.RegistryNamespace == "":
		return nil, errors.New("tenantclient: RegistryNamespace is required")
	case opts.Audience == "":
		return nil, errors.New("tenantclient: Audience is required: an unchecked audience is an unchecked credential")
	case opts.MintConfig == nil:
		return nil, errors.New("tenantclient: MintConfig is required")
	}
	codecs, err := newCodecs()
	if err != nil {
		return nil, err
	}
	return &Client{opts: opts, codecs: codecs}, nil
}

func newCodecs() (runtime.NegotiatedSerializer, error) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		authenticationv1.AddToScheme,
		v1alpha1.AddToScheme,
	} {
		if err := add(s); err != nil {
			return nil, fmt.Errorf("tenantclient: scheme: %w", err)
		}
	}
	metav1.AddToGroupVersion(s, schema.GroupVersion{Version: "v1"})
	return serializer.NewCodecFactory(s).WithoutConversion(), nil
}

func SAName(did string) string { return kcpclient.ServiceAccountName(did) }

func RowName(deviceKeyDID string) string { return rowPrefix + base32Lower(deviceKeyDID) }

func base32Lower(s string) string {
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(s)))
}

func (c *Client) Resolve(ctx context.Context, cred mint.DeviceCredential) (mint.Binding, error) {
	if cred.Token == "" {
		return mint.Binding{}, mint.ErrBindingUnknown
	}
	deviceKey, err := c.verifyDeviceCredential(ctx, cred.Token)
	if err != nil {
		return mint.Binding{}, err
	}

	row, err := c.readRow(ctx, deviceKey)
	if apierrors.IsNotFound(err) {
		return mint.Binding{}, mint.ErrBindingUnknown
	}
	if err != nil {
		return mint.Binding{}, err
	}
	if row.DeviceKey != deviceKey || row.DID == "" || row.Cluster == "" {
		return mint.Binding{}, mint.ErrBindingUnknown
	}

	cl, err := c.readCluster(ctx, row.DID, row.Cluster)
	if apierrors.IsNotFound(err) {
		return mint.Binding{}, mint.ErrBindingUnknown
	}
	if err != nil {
		return mint.Binding{}, err
	}

	core, err := c.tenantCore(row.DID, row.Cluster)
	if err != nil {
		return mint.Binding{}, err
	}

	name := SAName(row.DID)
	err = core.Get().Namespace(Namespace).Resource(serviceAccounts).Name(name).Do(ctx).Error()
	exists := true
	switch {
	case apierrors.IsNotFound(err):
		exists = false
	case err != nil:
		return mint.Binding{}, fmt.Errorf("tenantclient: read ServiceAccount %s/%s: %w", Namespace, name, err)
	}

	return mint.Binding{
		DID:      row.DID,
		Cluster:  row.Cluster,
		SA:       name,
		SAExists: exists,
		Instance: cl.Status.Instance,
		Running:  cl.Spec.Running,
	}, nil
}

func (c *Client) Issue(ctx context.Context, b mint.Binding, ttl time.Duration) (string, time.Time, error) {
	if b.DID == "" || b.Cluster == "" || b.SA == "" {
		return "", time.Time{}, mint.ErrBindingUnknown
	}
	if ttl < MinTokenTTL {
		return "", time.Time{}, fmt.Errorf("%w: %s", ErrTTLBelowFloor, ttl)
	}
	core, err := c.tenantCore(b.DID, b.Cluster)
	if err != nil {
		return "", time.Time{}, err
	}

	seconds := int64(ttl / time.Second)
	req := &authenticationv1.TokenRequest{
		TypeMeta: metav1.TypeMeta{APIVersion: authenticationv1.SchemeGroupVersion.String(), Kind: "TokenRequest"},
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{},
			ExpirationSeconds: &seconds,
			BoundObjectRef:    nil,
		},
	}

	var out authenticationv1.TokenRequest
	err = core.Post().Namespace(Namespace).Resource(serviceAccounts).Name(b.SA).SubResource(tokenSubresource).
		Body(req).Do(ctx).Into(&out)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("tenantclient: TokenRequest for %s/%s: %w", Namespace, b.SA, err)
	}
	if out.Status.Token == "" || out.Status.ExpirationTimestamp.IsZero() {
		return "", time.Time{}, fmt.Errorf("%w: %s/%s", ErrNoExpiration, Namespace, b.SA)
	}
	return out.Status.Token, out.Status.ExpirationTimestamp.Time, nil
}

func (c *Client) Record(ctx context.Context, r Row) error {
	if r.DeviceKey == "" || r.DID == "" || r.Cluster == "" {
		return errors.New("tenantclient: a binding row needs a device key, a DID and a cluster")
	}
	obj := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: RowName(r.DeviceKey)},
		Data: map[string]string{
			"deviceKey": r.DeviceKey,
			"did":       r.DID,
			"cluster":   r.Cluster,
		},
	}
	r2, err := c.registryAPI()
	if err != nil {
		return err
	}
	var out corev1.ConfigMap
	if err := r2.Post().Namespace(c.opts.RegistryNamespace).Resource("configmaps").Body(obj).Do(ctx).Into(&out); err != nil {
		return fmt.Errorf("tenantclient: record %s: %w", obj.Name, err)
	}
	return nil
}

func (c *Client) readRow(ctx context.Context, deviceKey string) (Row, error) {
	r, err := c.registryAPI()
	if err != nil {
		return Row{}, err
	}
	var cm corev1.ConfigMap
	if err := r.Get().Namespace(c.opts.RegistryNamespace).Resource("configmaps").Name(RowName(deviceKey)).Do(ctx).Into(&cm); err != nil {
		if apierrors.IsNotFound(err) {
			return Row{}, err
		}
		return Row{}, fmt.Errorf("tenantclient: read binding row %s: %w", RowName(deviceKey), err)
	}
	return Row{DeviceKey: cm.Data["deviceKey"], DID: cm.Data["did"], Cluster: cm.Data["cluster"]}, nil
}

func (c *Client) readCluster(ctx context.Context, did, cluster string) (*v1alpha1.Cluster, error) {
	r, err := c.kcpAPI(kcpclient.HomePath(did, cluster), v1alpha1.GroupVersion)
	if err != nil {
		return nil, err
	}
	var out v1alpha1.Cluster
	if err := r.Get().Resource("clusters").Name(cluster).Do(ctx).Into(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) registryAPI() (rest.Interface, error) {
	return c.kcpCore(c.opts.RegistryWorkspace)
}

func (c *Client) kcpCore(logicalCluster string) (rest.Interface, error) {
	return c.bind(rest.CopyConfig(c.opts.Registry), logicalCluster, corev1.SchemeGroupVersion)
}

func (c *Client) kcpAPI(logicalCluster string, gv schema.GroupVersion) (rest.Interface, error) {
	return c.bind(rest.CopyConfig(c.opts.Registry), logicalCluster, gv)
}

func (c *Client) tenantCore(did, cluster string) (rest.Interface, error) {
	cfg, err := c.opts.MintConfig(did, cluster)
	if err != nil {
		return nil, fmt.Errorf("tenantclient: no mint credential for cluster %q: %w", cluster, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("tenantclient: no mint credential for cluster %q", cluster)
	}
	return c.bind(rest.CopyConfig(cfg), "", corev1.SchemeGroupVersion)
}

func (c *Client) bind(cfg *rest.Config, logicalCluster string, gv schema.GroupVersion) (rest.Interface, error) {
	cfg.ContentType = "application/json"
	cfg.AcceptContentTypes = "application/json"
	cfg.GroupVersion = &gv
	cfg.APIPath = "/api"
	if logicalCluster != "" {
		cfg.APIPath = "/clusters/" + logicalCluster + "/api"
	}
	if gv.Group != "" {
		cfg.APIPath = strings.TrimSuffix(cfg.APIPath, "/api") + "/apis"
	}
	cfg.NegotiatedSerializer = c.codecs
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("tenantclient: %w", err)
	}
	return rest.RESTClientForConfigAndClient(cfg, httpClient)
}
