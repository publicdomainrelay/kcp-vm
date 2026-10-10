package eso

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

const FieldManager = "socialweb-clusterprovider"

const (
	DefaultControllerNamespace = "default"

	ClientSecretName = "openbao-eso-client-cert"

	StoreName = "openbao"

	CertKey = "tls.crt"

	KeyKey = "tls.key"

	CAKey = "ca.crt"

	OperatorGroupVersion = "external-secrets.io/v1"
)

var ErrNoOperator = errors.New(
	"eso: the cluster serves no " + OperatorGroupVersion + ", so the OpenBao ClusterSecretStore " +
		"and ExternalSecret have no kind to be applied as. That group is served by the External " +
		"Secrets Operator, and no operator is installed in this cluster")

func Installed(ctx context.Context, dyn dynamic.Interface) (bool, error) {
	_, err := dyn.Resource(externalSecretsResources["ClusterSecretStore"]).
		List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("eso: asking the cluster whether it serves %s: %w", OperatorGroupVersion, err)
}

func InstallOperator(ctx context.Context, dyn dynamic.Interface, install func(context.Context) error) error {
	ok, err := Installed(ctx, dyn)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if install == nil {
		return fmt.Errorf("%w (this provider configures no installer for it)", ErrNoOperator)
	}
	if err := install(ctx); err != nil {
		return fmt.Errorf("eso: installing the External Secrets Operator, whose %s the store "+
			"needs: %w", OperatorGroupVersion, err)
	}
	ok, err = Installed(ctx, dyn)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w (the installer ran and the group is still not served)", ErrNoOperator)
	}
	return nil
}

var ErrIncomplete = errors.New(
	"eso: the OpenBao-backed External Secrets install needs a manifest directory, the tenant " +
		"namespace to write into, the cluster vault's tenant-facing https URL, the vault server CA " +
		"and the ESO client certificate and key")

var secretResource = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

var externalSecretsResources = map[string]schema.GroupVersionResource{
	"ClusterSecretStore": {Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores"},
	"ExternalSecret":     {Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"},
	"SecretStore":        {Group: "external-secrets.io", Version: "v1", Resource: "secretstores"},
}

type Options struct {
	ManifestDir string

	Namespace string

	VaultServer string

	ServerCAPEM []byte

	ClientCertPEM []byte

	ClientKeyPEM []byte
}

func (o Options) validate() error {
	var problems []error
	for _, f := range []struct {
		name  string
		empty bool
	}{
		{"manifest directory", o.ManifestDir == ""},
		{"tenant namespace", o.Namespace == ""},
		{"vault server URL", o.VaultServer == ""},
		{"vault server CA", len(o.ServerCAPEM) == 0},
		{"client certificate", len(o.ClientCertPEM) == 0},
		{"client key", len(o.ClientKeyPEM) == 0},
	} {
		if f.empty {
			problems = append(problems, fmt.Errorf("%w: it names no %s", ErrIncomplete, f.name))
		}
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	u, err := url.Parse(o.VaultServer)
	if err != nil {
		return fmt.Errorf("%w: the vault server %q does not parse: %v", ErrIncomplete, o.VaultServer, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf(
			"%w: the vault server %q is not https with a host, and the ESO vault provider carries "+
				"the client certificate inside the TLS handshake, so a plaintext or hostless URL is "+
				"one on which auth/cert can never answer", ErrIncomplete, o.VaultServer)
	}
	return nil
}

func Load(dir string) ([]unstructured.Unstructured, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("eso: reading the manifest directory %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := strings.ToLower(filepath.Ext(e.Name())); ext != ".yaml" && ext != ".yml" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []unstructured.Unstructured
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("eso: reading %s: %w", path, err)
		}
		dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
		for {
			var obj map[string]any
			err := dec.Decode(&obj)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("eso: %s: %w", path, err)
			}
			if len(obj) == 0 {
				continue
			}
			u := unstructured.Unstructured{Object: obj}
			if u.GetKind() == "" || u.GetAPIVersion() == "" {
				return nil, fmt.Errorf("eso: %s holds an object with no apiVersion or kind", path)
			}
			if u.GetName() == "" {
				return nil, fmt.Errorf("eso: %s holds a %s with no name", path, u.GetKind())
			}
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("eso: the manifest directory %s holds no objects", dir)
	}
	return out, nil
}

func Resolve(u *unstructured.Unstructured, o Options) error {
	switch u.GetKind() {
	case "ClusterSecretStore", "SecretStore":
		if err := unstructured.SetNestedField(u.Object, o.VaultServer, "spec", "provider", "vault", "server"); err != nil {
			return err
		}
		if err := unstructured.SetNestedField(u.Object, base64.StdEncoding.EncodeToString(o.ServerCAPEM), "spec", "provider", "vault", "caBundle"); err != nil {
			return err
		}
		cert := map[string]any{"name": ClientSecretName, "namespace": o.Namespace, "key": CertKey}
		key := map[string]any{"name": ClientSecretName, "namespace": o.Namespace, "key": KeyKey}
		if err := unstructured.SetNestedMap(u.Object, cert, "spec", "provider", "vault", "auth", "cert", "clientCert"); err != nil {
			return err
		}
		if err := unstructured.SetNestedMap(u.Object, key, "spec", "provider", "vault", "auth", "cert", "secretRef"); err != nil {
			return err
		}
	case "ExternalSecret":
		u.SetNamespace(o.Namespace)
	default:
		return fmt.Errorf("eso: %s %s is not a kind this installer applies", u.GetKind(), u.GetName())
	}
	return nil
}

func Apply(ctx context.Context, dyn dynamic.Interface, o Options) error {
	if err := o.validate(); err != nil {
		return err
	}
	objs, err := Load(o.ManifestDir)
	if err != nil {
		return err
	}
	for i := range objs {
		if err := Resolve(&objs[i], o); err != nil {
			return err
		}
	}
	if err := applyClientSecret(ctx, dyn, o); err != nil {
		return err
	}
	for i := range objs {
		u := &objs[i]
		gvr, ok := externalSecretsResources[u.GetKind()]
		if !ok {
			return fmt.Errorf("eso: no resource is known for the %s %s this installer loaded", u.GetKind(), u.GetName())
		}
		if err := applyObject(ctx, dyn.Resource(gvr).Namespace(u.GetNamespace()), u); err != nil {
			return err
		}
	}
	return nil
}

func clientSecretBody(o Options) ([]byte, error) {
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		Type:     corev1.SecretTypeTLS,
		ObjectMeta: metav1.ObjectMeta{
			Name:      ClientSecretName,
			Namespace: o.Namespace,
		},
		Data: map[string][]byte{
			CertKey: o.ClientCertPEM,
			KeyKey:  o.ClientKeyPEM,
			CAKey:   o.ServerCAPEM,
		},
	}
	body, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("eso: marshalling the client certificate Secret: %w", err)
	}
	return body, nil
}

func applyClientSecret(ctx context.Context, dyn dynamic.Interface, o Options) error {
	body, err := clientSecretBody(o)
	if err != nil {
		return err
	}
	if _, err := dyn.Resource(secretResource).Namespace(o.Namespace).Patch(
		ctx, ClientSecretName, types.ApplyPatchType, body,
		metav1.PatchOptions{FieldManager: FieldManager, Force: ptr(true)},
	); err != nil {
		return fmt.Errorf("eso: applying the client certificate Secret %s/%s: %w", o.Namespace, ClientSecretName, err)
	}
	return nil
}

func applyObject(ctx context.Context, ri dynamic.ResourceInterface, u *unstructured.Unstructured) error {
	body, err := json.Marshal(u.Object)
	if err != nil {
		return fmt.Errorf("eso: marshalling the %s %s: %w", u.GetKind(), u.GetName(), err)
	}
	if _, err := ri.Patch(
		ctx, u.GetName(), types.ApplyPatchType, body,
		metav1.PatchOptions{FieldManager: FieldManager, Force: ptr(true)},
	); err != nil {
		return fmt.Errorf("eso: applying the %s %s: %w", u.GetKind(), u.GetName(), err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
