//go:build linux

package clusterprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/eso"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
)

const tenantGrantPath = "../../deploy/tenant-grant.yaml"

func tenantGrantDoc(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(tenantGrantPath)
	if err != nil {
		t.Fatalf("read %s: %v", tenantGrantPath, err)
	}
	return raw
}

func renderedGrant(t *testing.T, ownerDID string) []unstructured.Unstructured {
	t.Helper()
	rendered, err := kcpclient.RenderTenantGrant(string(tenantGrantDoc(t)), kcpclient.GrantScope{
		OwnerDID: ownerDID,
		ESOUser:  eso.ControllerUser,
	})
	if err != nil {
		t.Fatalf("the deployed grant is not renderable for %s: %v", ownerDID, err)
	}
	objs, err := GrantObjects([]byte(rendered))
	if err != nil {
		t.Fatalf("GrantObjects: %v", err)
	}
	return objs
}

const grantOwner = "did:plc:grantowner0000000000000000"

func TestTheGrantIsRenderedForTheClustersOwner(t *testing.T) {
	if !strings.Contains(string(tenantGrantDoc(t)), kcpclient.GrantOwnerPlaceholder) {
		t.Fatalf("deploy/tenant-grant.yaml carries no %s, so the bootstrap would apply a grant "+
			"scoped to whatever name is written in the file rather than to the cluster's owner",
			kcpclient.GrantOwnerPlaceholder)
	}
	objs := renderedGrant(t, grantOwner)
	owner := kcpclient.ServiceAccountName(grantOwner)

	var sawTokenRule, sawReadRule, sawOwnerBinding bool
	for i := range objs {
		o := &objs[i]
		switch o.GetKind() {
		case "ServiceAccount":
			if o.GetName() != MintServiceAccount || o.GetNamespace() != kcpclient.TenantNamespace {
				t.Errorf("the mint ServiceAccount is %s/%s, want %s/%s",
					o.GetNamespace(), o.GetName(), kcpclient.TenantNamespace, MintServiceAccount)
			}
		case "Role":
			rules, _, _ := unstructured.NestedSlice(o.Object, "rules")
			for _, rule := range rules {
				r, _ := rule.(map[string]any)
				names, _ := r["resourceNames"].([]any)
				if len(names) != 1 || names[0] != owner {
					t.Errorf("the grant's %v rule names %v, want exactly [%s]: it is scoped to ONE "+
						"derived ServiceAccount and a wildcard here is total impersonation",
						r["resources"], names, owner)
				}
				if roleNamesTheTokenSubresource(r) {
					sawTokenRule = true
				} else if _, ok := r["resources"].([]any); ok {
					sawReadRule = true
				}
			}
		case "ClusterRoleBinding":
			if o.GetName() != "socialweb-owner" {
				t.Errorf("the owner binding is named %q, want socialweb-owner", o.GetName())
			}
			subjects, _, _ := unstructured.NestedSlice(o.Object, "subjects")
			if len(subjects) != 1 {
				t.Fatalf("the owner binding carries %d subjects, want exactly the owner", len(subjects))
			}
			s, _ := subjects[0].(map[string]any)
			if s["name"] != owner || s["namespace"] != kcpclient.TenantNamespace {
				t.Errorf("the owner binding binds %v/%v, want %s/%s",
					s["namespace"], s["name"], kcpclient.TenantNamespace, owner)
			}
			ref, _, _ := unstructured.NestedMap(o.Object, "roleRef")
			if ref["kind"] != "ClusterRole" || ref["name"] != "cluster-admin" {
				t.Errorf("the owner binding's roleRef is %v, want ClusterRole cluster-admin: it is "+
					"the owner's own cluster and its credential must be able to create namespaces, "+
					"exec into pods and drive a runner", ref)
			}
			sawOwnerBinding = true
		}
	}
	if !sawTokenRule || !sawReadRule {
		t.Errorf("the grant is missing a scoped rule: token=%v read=%v", sawTokenRule, sawReadRule)
	}
	if !sawOwnerBinding {
		t.Fatal("the grant carries no ClusterRoleBinding, so a real user's minted token " +
			"authenticates and is authorized for nothing")
	}

	other, err := kcpclient.RenderTenantGrant(string(tenantGrantDoc(t)), kcpclient.GrantScope{
		OwnerDID: "did:plc:someoneelse00000000000000",
		ESOUser:  eso.ControllerUser,
	})
	if err != nil {
		t.Fatalf("RenderTenantGrant: %v", err)
	}
	if other == string(tenantGrantDoc(t)) || strings.Contains(other, owner) {
		t.Fatalf("rendering the grant for a different owner produced the same names, so the " +
			"substitution is not reading the owner it was handed")
	}
}

func TestTheGrantAdmitsTheExternalSecretsControllerBesideTheControlPlaneAndNoServiceAccount(t *testing.T) {
	if !strings.Contains(string(tenantGrantDoc(t)), kcpclient.GrantESOUserPlaceholder) {
		t.Fatalf("deploy/tenant-grant.yaml carries no %s, so the policy admits whichever username "+
			"the file hardcodes", kcpclient.GrantESOUserPlaceholder)
	}
	rendered, err := kcpclient.RenderTenantGrant(string(tenantGrantDoc(t)), kcpclient.GrantScope{
		OwnerDID: grantOwner,
		ESOUser:  eso.ControllerUser,
	})
	if err != nil {
		t.Fatalf("RenderTenantGrant: %v", err)
	}
	if want := "'" + eso.ControllerUser + "'"; !strings.Contains(rendered, want) {
		t.Fatalf("the grant does not admit %s, so the controller beside the control plane is denied "+
			"every Secret it materialises in %s", want, kcpclient.TenantNamespace)
	}
	if strings.Contains(rendered, "system:serviceaccount:default:external-secrets") {
		t.Fatal("the grant still admits the in-tenant controller's ServiceAccount, which a tenant who " +
			"can create a ServiceAccount of that name inherits")
	}
	if strings.Contains(rendered, "system:serviceaccount:__") || strings.Contains(rendered, "__SOCIALWEB_ESO") {
		t.Fatal("the grant carries an unrendered placeholder")
	}
	for _, bad := range []string{"", "x' || true || 'y", "a\\b", "system:socialweb:eso "} {
		if _, err := kcpclient.RenderTenantGrant(string(tenantGrantDoc(t)), kcpclient.GrantScope{
			OwnerDID: grantOwner, ESOUser: bad,
		}); err == nil {
			t.Fatalf("RenderTenantGrant accepted the External Secrets username %q, which the policy "+
				"compares inside a CEL string literal", bad)
		}
	}
}

func TestBootstrapEnsuresTheServiceAccountsTheGrantNames(t *testing.T) {
	objs := renderedGrant(t, grantOwner)
	ctx := context.Background()
	core := fake.NewSimpleClientset()
	if err := ensureGrantServiceAccounts(ctx, core, objs); err != nil {
		t.Fatalf("ensureGrantServiceAccounts: %v", err)
	}
	named := kcpclient.ServiceAccountName(grantOwner)
	got, err := core.CoreV1().ServiceAccounts("users").Get(ctx, named, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the grant's token rule names %s and nothing created it, so the mint can "+
			"authorize the request and still fail on a missing ServiceAccount: %v", named, err)
	}
	if got.Namespace != "users" {
		t.Fatalf("%s was created in namespace %q, want users", named, got.Namespace)
	}
	if err := ensureGrantServiceAccounts(ctx, core, objs); err != nil {
		t.Fatalf("a second bootstrap must be idempotent: %v", err)
	}
}

func TestGrantObjectsRefusesAKindTheBootstrapMustNotApply(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"a ClusterRole", "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata: {name: wider}\n", "ClusterRole"},
		{"a Namespace", "apiVersion: v1\nkind: Namespace\nmetadata: {name: users}\n", "Namespace"},
		{"a wrong apiVersion", "apiVersion: rbac.authorization.k8s.io/v1beta1\nkind: Role\nmetadata: {name: r}\n", "v1beta1"},
		{"no name", "apiVersion: v1\nkind: ServiceAccount\nmetadata: {}\n", "no name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := GrantObjects([]byte(tc.doc))
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not name %q: %v", tc.want, err)
			}
		})
	}
	if _, err := GrantObjects([]byte("# nothing here\n")); err == nil {
		t.Fatal("an empty document was accepted, which would report a successful bootstrap " +
			"that applied nothing")
	}
}

func TestWriteMintKubeconfigIsWhatTheGatewayReadsBack(t *testing.T) {
	dir := t.TempDir()
	cred := &rest.Config{
		Host:        "https://10.0.0.1:6443",
		BearerToken: "a-token",
		TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("-----BEGIN CERTIFICATE-----\nnot really\n-----END CERTIFICATE-----\n"),
		},
	}
	if err := WriteMintKubeconfig(dir, "live", cred); err != nil {
		t.Fatalf("WriteMintKubeconfig: %v", err)
	}
	path := filepath.Join(dir, "live")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the credential is not at the path the label names: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("the mint credential is %04o: it holds a standing bearer token and nothing "+
			"downstream could detect a world-readable copy", got)
	}
	got, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("client-go cannot read back what this wrote: %v", err)
	}
	if got.Host != cred.Host || got.BearerToken != cred.BearerToken {
		t.Errorf("read back host=%q token=%q", got.Host, got.BearerToken)
	}
	if len(got.TLSClientConfig.CAData) == 0 {
		t.Error("the CA did not survive the round trip, so the credential could only be used " +
			"with verification switched off")
	}
}

func TestWriteMintKubeconfigRefusesWhatItCannotWriteHonestly(t *testing.T) {
	dir := t.TempDir()
	good := &rest.Config{Host: "https://h:6443", BearerToken: "t"}
	for _, tc := range []struct {
		name    string
		cluster string
		cfg     *rest.Config
		want    string
	}{
		{"a traversal", "../../etc/passwd", good, "DNS-1123"},
		{"a slash", "a/b", good, "DNS-1123"},
		{"empty", "", good, "DNS-1123"},
		{"no server", "live", &rest.Config{BearerToken: "t"}, "server"},
		{"no token", "live", &rest.Config{Host: "https://h:6443"}, "anonymous"},
		{"nil", "live", nil, "server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteMintKubeconfig(dir, tc.cluster, tc.cfg)
			if err == nil {
				t.Fatalf("%s was written", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not name %q: %v", tc.want, err)
			}
		})
	}
	if names, _ := os.ReadDir(dir); len(names) != 0 {
		t.Fatalf("a refused write left %d files behind", len(names))
	}
}

func TestNewRefusesAPartialTenantBootstrap(t *testing.T) {
	base := func() Options {
		return Options{Registry: &fakeClusters{}, Decider: decider(t, &fakeHost{})}
	}
	tenant := func(o *Options) {
		o.Tenant = func(context.Context, Ref, *v1alpha1.Cluster) (*rest.Config, error) { return nil, nil }
	}

	for _, tc := range []struct {
		name string
		set  func(*Options)
	}{
		{"credential only", tenant},
		{"grant only", func(o *Options) { o.GrantPath = tenantGrantPath }},
		{"mint dir only", func(o *Options) { o.MintDir = t.TempDir() }},
		{"credential and grant", func(o *Options) { tenant(o); o.GrantPath = tenantGrantPath }},
		{"credential and dir", func(o *Options) { tenant(o); o.MintDir = t.TempDir() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base()
			tc.set(&o)
			if _, err := New(o); !errors.Is(err, ErrNoBootstrap) {
				t.Fatalf("New accepted a partial bootstrap: err=%v", err)
			}
		})
	}

	o := base()
	tenant(&o)
	o.GrantPath = tenantGrantPath
	o.MintDir = t.TempDir()
	p, err := New(o)
	if err != nil {
		t.Fatalf("New with the whole tenant set: %v", err)
	}
	if p.opts.OnReady == nil {
		t.Fatal("all three tenant fields are set and OnReady is still nil, so nothing would " +
			"ever run the bootstrap")
	}
	if p.opts.TokenTimeout != DefaultTokenTimeout {
		t.Errorf("TokenTimeout defaulted to %v", p.opts.TokenTimeout)
	}

	mine := errors.New("mine")
	o.OnReady = func(context.Context, Ref, *v1alpha1.Cluster) error { return mine }
	o.GrantPath = tenantGrantPath
	p2, err := New(o)
	if err != nil {
		t.Fatalf("New with a caller OnReady: %v", err)
	}
	if err := p2.opts.OnReady(context.Background(), Ref{}, nil); !errors.Is(err, mine) {
		t.Fatalf("the caller's OnReady was replaced by the bootstrap: %v", err)
	}
}

func TestBootstrapRefusesWithoutTheWorldItNeeds(t *testing.T) {
	p, err := New(Options{Registry: &fakeClusters{}, Decider: decider(t, &fakeHost{})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Bootstrap(context.Background(), Ref{Name: "live"}, nil); !errors.Is(err, ErrNoBootstrap) {
		t.Fatalf("Bootstrap without a tenant credential: %v", err)
	}
}
