package clusterprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
)

var ErrNoBootstrap = errors.New("clusterprovider: the tenant bootstrap needs a tenant credential, a grant path and a mint directory")

const fieldManager = "socialweb-clusterprovider"

const mintSecretName = "gwmint-mint-credential"

const MintServiceAccount = "gwmint"

const DefaultTokenTimeout = 30 * time.Second

var grantResources = map[string]schema.GroupVersionResource{
	"ServiceAccount":                   {Group: "", Version: "v1", Resource: "serviceaccounts"},
	"Role":                             {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	"RoleBinding":                      {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	"ValidatingAdmissionPolicy":        {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"},
	"ValidatingAdmissionPolicyBinding": {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"},
}

func GrantObjects(doc []byte) ([]unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096)
	var out []unstructured.Unstructured
	for {
		var raw map[string]any
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("clusterprovider: the grant document: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		u := unstructured.Unstructured{Object: raw}
		gvk := u.GroupVersionKind()
		gvr, ok := grantResources[gvk.Kind]
		if !ok {
			return nil, fmt.Errorf(
				"clusterprovider: the grant document holds a %q, which is not one of the %d kinds "+
					"this bootstrap applies; a new kind is a decision (see grantResources), not an apply",
				gvk.Kind, len(grantResources))
		}
		if want := gvr.GroupVersion().String(); gvk.GroupVersion().String() != want {
			return nil, fmt.Errorf(
				"clusterprovider: the grant's %s is %s and this bootstrap applies it as %s",
				gvk.Kind, gvk.GroupVersion().String(), want)
		}
		if u.GetName() == "" {
			return nil, fmt.Errorf("clusterprovider: the grant holds a %s with no name", gvk.Kind)
		}
		out = append(out, u)
	}
	if len(out) == 0 {
		return nil, errors.New("clusterprovider: the grant document holds no objects")
	}
	return out, nil
}

func applyGrant(ctx context.Context, dyn dynamic.Interface, objs []unstructured.Unstructured) error {
	for i := range objs {
		u := &objs[i]
		gvr := grantResources[u.GetKind()]
		body, err := json.Marshal(u.Object)
		if err != nil {
			return fmt.Errorf("clusterprovider: marshalling the %s %s: %w", u.GetKind(), u.GetName(), err)
		}
		_, err = dyn.Resource(gvr).Namespace(u.GetNamespace()).Patch(
			ctx, u.GetName(), types.ApplyPatchType, body,
			metav1.PatchOptions{FieldManager: fieldManager, Force: ptr(true)},
		)
		if err != nil {
			return fmt.Errorf("clusterprovider: applying the %s %s: %w", u.GetKind(), u.GetName(), err)
		}
	}
	return nil
}

func ensureGrantServiceAccounts(ctx context.Context, core kubernetes.Interface, objs []unstructured.Unstructured) error {
	for i := range objs {
		o := &objs[i]
		if o.GetKind() != "Role" {
			continue
		}
		rules, ok, err := unstructured.NestedSlice(o.Object, "rules")
		if err != nil {
			return fmt.Errorf("clusterprovider: reading the rules of the %s %s: %w", o.GetKind(), o.GetName(), err)
		}
		if !ok {
			continue
		}
		namespace := o.GetNamespace()
		for _, rule := range rules {
			r, _ := rule.(map[string]any)
			if !roleNamesTheTokenSubresource(r) {
				continue
			}
			names, _ := r["resourceNames"].([]any)
			for _, n := range names {
				name, _ := n.(string)
				if name == "" {
					continue
				}
				_, err := core.CoreV1().ServiceAccounts(namespace).Create(ctx,
					&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
					metav1.CreateOptions{FieldManager: fieldManager})
				if err != nil && !apierrors.IsAlreadyExists(err) {
					return fmt.Errorf(
						"clusterprovider: the grant mints for the ServiceAccount %s/%s and it does "+
							"not exist; creating the identity the grant names: %w", namespace, name, err)
				}
			}
		}
	}
	return nil
}

func roleNamesTheTokenSubresource(rule map[string]any) bool {
	resources, _ := rule["resources"].([]any)
	for _, r := range resources {
		if s, _ := r.(string); s == "serviceaccounts/token" {
			return true
		}
	}
	return false
}

func obtainMintCredential(ctx context.Context, core kubernetes.Interface, host string, timeout time.Duration) (*rest.Config, error) {
	secrets := core.CoreV1().Secrets(kcpclient.TenantNamespace)
	secret, err := secrets.Get(ctx, mintSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret, err = secrets.Create(ctx, mintTokenSecret(kcpclient.TenantNamespace), metav1.CreateOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf(
			"clusterprovider: the bootstrap could not write the mint Secret in %s/%s. "+
				"A 403 here that names ValidatingAdmissionPolicy 'socialweb-mint-identity' is the "+
				"policy refusing this CREDENTIAL, not this code: the carve-out admits the "+
				"provisioning cluster-admin (deploy/tenant-grant.yaml, rule (2)) and nothing else: %w",
			kcpclient.TenantNamespace, mintSecretName, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		if token := secret.Data[corev1.ServiceAccountTokenKey]; len(token) > 0 {
			if len(secret.Data[corev1.ServiceAccountRootCAKey]) == 0 {
				return nil, fmt.Errorf(
					"clusterprovider: the mint Secret %s/%s holds a token and no %s, so the "+
						"credential it yields could only be used with verification switched off",
					kcpclient.TenantNamespace, mintSecretName, corev1.ServiceAccountRootCAKey)
			}
			return &rest.Config{
				Host:        host,
				BearerToken: string(token),
				TLSClientConfig: rest.TLSClientConfig{
					CAData: secret.Data[corev1.ServiceAccountRootCAKey],
				},
			}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"clusterprovider: the mint Secret %s/%s was written but is still empty after %s; "+
					"the token controller populates it, so an empty one means the controller never "+
					"saw it -- most often because a ValidatingAdmissionPolicy refused the "+
					"controller's own UPDATE",
				kcpclient.TenantNamespace, mintSecretName, timeout)
		}
		time.Sleep(500 * time.Millisecond)
		secret, err = secrets.Get(ctx, mintSecretName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("clusterprovider: re-reading the mint Secret: %w", err)
		}
	}
}

func mintTokenSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mintSecretName,
			Namespace: namespace,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: MintServiceAccount,
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
}

func WriteMintKubeconfig(dir, cluster string, cfg *rest.Config) error {
	if errs := validation.IsDNS1123Label(cluster); len(errs) > 0 {
		return fmt.Errorf("clusterprovider: %q is not a DNS-1123 label, so it cannot name a mint credential: %s", cluster, errs[0])
	}
	if cfg == nil || cfg.Host == "" {
		return errors.New("clusterprovider: a mint credential with no server address is not writable")
	}
	if cfg.BearerToken == "" {
		return errors.New("clusterprovider: a mint credential with no token would send anonymous and fail as an authentication error")
	}
	doc, err := mintKubeconfig(cluster, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("clusterprovider: creating the mint directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, cluster)
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		return fmt.Errorf("clusterprovider: writing the mint credential for %q: %w", cluster, err)
	}
	return nil
}

func mintKubeconfig(cluster string, cfg *rest.Config) ([]byte, error) {
	out := clientcmdapi.NewConfig()
	out.Clusters[cluster] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.TLSClientConfig.CAData,
	}
	out.AuthInfos[cluster] = &clientcmdapi.AuthInfo{Token: cfg.BearerToken}
	out.Contexts[cluster] = &clientcmdapi.Context{Cluster: cluster, AuthInfo: cluster}
	out.CurrentContext = cluster
	doc, err := clientcmd.Write(*out)
	if err != nil {
		return nil, fmt.Errorf("clusterprovider: rendering the mint credential for %q: %w", cluster, err)
	}
	return doc, nil
}

func (p *Provider) Bootstrap(ctx context.Context, ref Ref, cl *v1alpha1.Cluster) error {
	if p.opts.Tenant == nil || p.opts.GrantPath == "" || p.opts.MintDir == "" {
		return fmt.Errorf("%w (cluster %s)", ErrNoBootstrap, ref.Name)
	}
	cfg, err := p.opts.Tenant(ctx, ref, cl)
	if err != nil {
		return fmt.Errorf("clusterprovider: no tenant credential for %s: %w", ref.Name, err)
	}
	if cfg == nil {
		return fmt.Errorf("clusterprovider: no tenant credential for %s", ref.Name)
	}

	grant, err := os.ReadFile(p.opts.GrantPath)
	if err != nil {
		return fmt.Errorf("clusterprovider: reading the tenant grant: %w", err)
	}
	objs, err := GrantObjects(grant)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("clusterprovider: a dynamic client for %s: %w", ref.Name, err)
	}
	if err := applyGrant(ctx, dyn, objs); err != nil {
		return err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("clusterprovider: a client for %s: %w", ref.Name, err)
	}
	if err := ensureGrantServiceAccounts(ctx, core, objs); err != nil {
		return err
	}
	credential, err := obtainMintCredential(ctx, core, cfg.Host, p.opts.TokenTimeout)
	if err != nil {
		return err
	}
	if err := WriteMintKubeconfig(p.opts.MintDir, ref.Name, credential); err != nil {
		return err
	}
	if err := p.SetMintConfig(ref.Name, credential); err != nil {
		return err
	}
	if p.opts.InstallTenant != nil {
		if err := p.opts.InstallTenant(ctx, ref.Name, cfg); err != nil {
			return fmt.Errorf("clusterprovider: installing the tenant add-ons for %s after its "+
				"bootstrap: %w", ref.Name, err)
		}
	}
	p.opts.Log.Info("tenant bootstrap complete",
		"cluster", ref.Name, "did", ref.DID,
		"grant", p.opts.GrantPath, "objects", len(objs),
		"mintCredential", filepath.Join(p.opts.MintDir, ref.Name),
		"credentialHost", credential.Host, "statusURL", cl.Status.URL,
	)
	return nil
}

func ptr[T any](v T) *T { return &v }
