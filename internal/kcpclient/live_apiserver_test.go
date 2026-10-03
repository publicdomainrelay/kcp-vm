package kcpclient

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

func TestTheNotFoundDiscriminatorAgainstALiveApiserver(t *testing.T) {
	livegate.Short(t, "this test needs a live apiserver and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	kubeconfig := os.Getenv("SOCIALWEB_LIVE_KUBECONFIG")
	host := os.Getenv("SOCIALWEB_LIVE_APISERVER")
	did := os.Getenv("SOCIALWEB_LIVE_DID")
	label := os.Getenv("SOCIALWEB_LIVE_CLUSTER")
	tenantKubeconfig := os.Getenv("SOCIALWEB_LIVE_TENANT_KUBECONFIG")
	for _, env := range []struct{ name, value string }{
		{"SOCIALWEB_LIVE_KUBECONFIG", kubeconfig},
		{"SOCIALWEB_LIVE_APISERVER", host},
		{"SOCIALWEB_LIVE_DID", did},
		{"SOCIALWEB_LIVE_CLUSTER", label},
		{"SOCIALWEB_LIVE_TENANT_KUBECONFIG", tenantKubeconfig},
	} {
		if env.value == "" {
			livegate.Require(t, "NOT RUN: %s is unset.\n"+
				"  This guards the 404 discriminator in DeleteServiceAccount, which decides "+
				"whether a revocation COMPLETES or reports Pending forever. The delete travels "+
				"through the provisioned account's mounted child to its tenant apiserver, and the "+
				"mount forwards only a bearer header, so the tenant kubeconfig is where the bearer "+
				"is issued.", env.name)
		}
	}

	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	ctx := context.Background()
	admin, err := New(Options{Host: host, RestConfig: restCfg})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := admin.Ensure(ctx, gateway.Caller(did), label); err != nil {
		t.Fatalf("provisioning the account's mounted child: %v", err)
	}

	tenantCfg, err := clientcmd.BuildConfigFromFlags("", tenantKubeconfig)
	if err != nil {
		t.Fatalf("tenant kubeconfig: %v", err)
	}
	tenant, err := kubernetes.NewForConfig(tenantCfg)
	if err != nil {
		t.Fatalf("tenant clientset: %v", err)
	}
	bearer := tenantBearer(t, ctx, tenant)
	name := ServiceAccountName(did)

	mountCfg := rest.CopyConfig(restCfg)
	mountCfg.Host = strings.TrimSuffix(host, "/") + "/clusters/" + MountPath(did, label)
	mountCfg.CertData, mountCfg.CertFile, mountCfg.KeyData, mountCfg.KeyFile = nil, "", nil, ""
	mountCfg.Username, mountCfg.Password, mountCfg.BearerToken, mountCfg.BearerTokenFile = "", "", bearer, ""
	mountCfg.Impersonate = rest.ImpersonationConfig{}
	typed, err := kubernetes.NewForConfig(mountCfg)
	if err != nil {
		t.Fatalf("clientset through the mount: %v", err)
	}
	if _, err := typed.CoreV1().ServiceAccounts(TenantNamespace).Create(ctx,
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{}); err != nil &&
		!apierrors.IsAlreadyExists(err) {
		t.Fatalf("create serviceaccount through the mount: %v", err)
	}
	c, err := New(Options{Host: host, RestConfig: mountCfg})
	if err != nil {
		t.Fatalf("New through the mount credential: %v", err)
	}

	t.Run("a real delete succeeds", func(t *testing.T) {
		if err := c.DeleteServiceAccount(ctx, did, label); err != nil {
			t.Fatalf("deleting an existing ServiceAccount failed: %v", err)
		}
	})

	t.Run("and the SECOND delete is accepted -- THIS IS THE DISCRIMINATOR", func(t *testing.T) {
		err := c.DeleteServiceAccount(ctx, did, label)
		if err != nil {
			t.Fatalf("*** A REAL APISERVER'S 404 WAS REJECTED: %v\n\n"+
				"The discriminator requires details.kind/details.name on the Status. If this "+
				"failed, a real apiserver does not send them -- so a revocation whose delete "+
				"SUCCEEDED would report RevocationPending FOREVER, retrying a delete that "+
				"already happened. The branch is silent and security-relevant.", err)
		}
	})
}

const liveProbe = "gate-delete-probe"

func tenantBearer(t *testing.T, ctx context.Context, tenant kubernetes.Interface) string {
	t.Helper()
	if _, err := tenant.CoreV1().ServiceAccounts(TenantNamespace).Create(ctx,
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: liveProbe, Namespace: TenantNamespace}},
		metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("probe ServiceAccount: %v", err)
	}
	if _, err := tenant.RbacV1().Roles(TenantNamespace).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: liveProbe, Namespace: TenantNamespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"serviceaccounts"},
			Verbs:     []string{"get", "list", "create", "delete"},
		}},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("probe Role: %v", err)
	}
	if _, err := tenant.RbacV1().RoleBindings(TenantNamespace).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: liveProbe, Namespace: TenantNamespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: liveProbe},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: liveProbe, Namespace: TenantNamespace}},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("probe RoleBinding: %v", err)
	}
	if _, err := tenant.CoreV1().Secrets(TenantNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        liveProbe,
			Namespace:   TenantNamespace,
			Annotations: map[string]string{corev1.ServiceAccountNameKey: liveProbe},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("probe token Secret: %v", err)
	}
	t.Cleanup(func() {
		_ = tenant.CoreV1().Secrets(TenantNamespace).Delete(ctx, liveProbe, metav1.DeleteOptions{})
		_ = tenant.RbacV1().RoleBindings(TenantNamespace).Delete(ctx, liveProbe, metav1.DeleteOptions{})
		_ = tenant.RbacV1().Roles(TenantNamespace).Delete(ctx, liveProbe, metav1.DeleteOptions{})
		_ = tenant.CoreV1().ServiceAccounts(TenantNamespace).Delete(ctx, liveProbe, metav1.DeleteOptions{})
	})

	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := tenant.CoreV1().Secrets(TenantNamespace).Get(ctx, liveProbe, metav1.GetOptions{})
		if err == nil && len(got.Data[corev1.ServiceAccountTokenKey]) > 0 {
			return string(got.Data[corev1.ServiceAccountTokenKey])
		}
		if time.Now().After(deadline) {
			t.Fatalf("the probe token Secret was never populated: err=%v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestTheSecondLoginAgainstALiveKcp(t *testing.T) {
	livegate.Short(t, "this test needs a live kcp and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	kubeconfig := os.Getenv("SOCIALWEB_LIVE_KUBECONFIG")
	host := os.Getenv("SOCIALWEB_LIVE_APISERVER")
	if kubeconfig == "" || host == "" {
		livegate.Require(t, "NOT RUN: set SOCIALWEB_LIVE_KUBECONFIG and SOCIALWEB_LIVE_APISERVER.\n"+
			"  This is the second-login case end to end: the first login must WRITE the "+
			"account's AtprotoIdentity, because the second login's deny read finds a "+
			"workspace that exists and a record that does not -- RecordMissing, and a "+
			"refusal. Without a cluster whose account type binds the atprotoidentities "+
			"export, that write is a 404 and none of this can be observed.")
	}

	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	c, err := New(Options{Host: host, RestConfig: restCfg})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	const (
		did   = "did:web:second-login.live.probe"
		label = "live"
	)

	eraseTree(t, ctx, c, did, label)

	if err := c.Ensure(ctx, gateway.Caller(did), label); err != nil {
		t.Fatalf("first login against a live kcp: %v", err)
	}

	state, err := c.Lookup(ctx, did)
	if err != nil {
		t.Fatalf("Lookup after a first login: %v", err)
	}
	if state != revocation.DenyGranted {
		t.Fatalf("after a first login a live kcp reports %q, want %q.\n\nNothing on the "+
			"creation path wrote the account's AtprotoIdentity, so the workspace exists and "+
			"its deny record does not: RecordMissing, which Admit refuses -- every login "+
			"after the first is refused forever with `revocation: account identity record "+
			"is missing`.", state, revocation.DenyGranted)
	}

	if err := c.Ensure(ctx, gateway.Caller(did), label); err != nil {
		t.Fatalf("SECOND login refused by a live kcp: %v", err)
	}
	t.Cleanup(func() { eraseTree(t, ctx, c, did, label) })
}

func eraseTree(t *testing.T, ctx context.Context, c *Client, did, label string) {
	t.Helper()
	deleteWorkspace := func(parent, name string) {
		r, err := c.resource(parent, tenancyResource)
		if err != nil {
			t.Logf("bind %s: %v", parent, err)
			return
		}
		if err := r.Delete().Resource("workspaces").Name(name).Do(ctx).Error(); err != nil && !apierrors.IsNotFound(err) {
			t.Logf("delete workspace %s in %s: %v", name, parent, err)
		}
	}
	if home, err := c.resource(HomePath(did, label), socialwebResource); err == nil {
		_ = home.Delete().Resource("clusters").Name(label).Do(ctx).Error()
	}
	deleteWorkspace(HomePath(did, label), ChildLabel)
	deleteWorkspace(AccountPath(did), label)
	deleteWorkspace(RootWorkspace, AccountHash(did))

	for i := 0; i < 100; i++ {
		if _, err := c.readWorkspace(ctx, RootWorkspace, AccountHash(did)); apierrors.IsNotFound(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("the account workspace %s is still there after the delete; the next run's Lookup "+
		"may refuse it as terminating", AccountPath(did))
}
