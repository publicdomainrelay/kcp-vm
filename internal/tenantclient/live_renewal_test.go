package tenantclient_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/syntax"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/devicekey"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantclient"
)

func TestTheRenewalPathAgainstRealInfrastructure(t *testing.T) {
	livegate.Short(t, "this test runs ADR 0020's renewal path against real kcp and tenant infrastructure and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	kcpKubeconfig := os.Getenv("SOCIALWEB_LIVE_KCP_KUBECONFIG")
	tenantKubeconfig := os.Getenv("SOCIALWEB_LIVE_TENANT_KUBECONFIG")
	registryWorkspace := os.Getenv("SOCIALWEB_LIVE_REGISTRY_WORKSPACE")
	registryNamespace := os.Getenv("SOCIALWEB_LIVE_REGISTRY_NAMESPACE")
	did := os.Getenv("SOCIALWEB_LIVE_DID")
	cluster := os.Getenv("SOCIALWEB_LIVE_CLUSTER")
	for _, env := range []struct{ name, value string }{
		{"SOCIALWEB_LIVE_KCP_KUBECONFIG", kcpKubeconfig},
		{"SOCIALWEB_LIVE_TENANT_KUBECONFIG", tenantKubeconfig},
		{"SOCIALWEB_LIVE_REGISTRY_WORKSPACE", registryWorkspace},
		{"SOCIALWEB_LIVE_REGISTRY_NAMESPACE", registryNamespace},
		{"SOCIALWEB_LIVE_DID", did},
		{"SOCIALWEB_LIVE_CLUSTER", cluster},
	} {
		if env.value == "" {
			where, crName := "<whatever SOCIALWEB_LIVE_DID and SOCIALWEB_LIVE_CLUSTER name>", "<the cluster label>"
			if did != "" && cluster != "" {
				where, crName = kcpclient.HomePath(did, cluster), cluster
			}
			livegate.Require(t, "NOT RUN: %s is unset.\n"+
				"  This is ADR 0020's renewal path -- Grant -> Resolve -> Issue -- which has "+
				"only ever run against fakes. Nothing here is simulated: it writes a binding "+
				"row to a real kcp, reads a real Cluster CR and a real ServiceAccount, and "+
				"posts a real TokenRequest.\n"+
				"  It requires a Cluster CR named %s in the kcp home workspace %s, and a "+
				"tenant apiserver whose ns=%s exists. See the file header for the full env "+
				"list.", env.name, crName, where, tenantclient.Namespace)
		}
	}

	ctx := context.Background()
	kcpCfg := liveConfig(t, kcpKubeconfig)
	tenantCfg := liveConfig(t, tenantKubeconfig)

	const audience = "did:web:gateway.live.test#socialweb"
	const execCommand = "/usr/local/bin/socialweb-credential-helper"
	const execEnvName = "SOCIALWEB_DEVICE_CREDENTIAL"

	store, err := tenantclient.New(tenantclient.Options{
		Registry:          kcpCfg,
		RegistryWorkspace: registryWorkspace,
		RegistryNamespace: registryNamespace,
		Audience:          audience,
		MintConfig: func(string, string) (*rest.Config, error) {
			c := rest.CopyConfig(tenantCfg)
			c.Impersonate = rest.ImpersonationConfig{
				UserName: "system:serviceaccount:users:gwmint",
			}
			return c, nil
		},
	})
	if err != nil {
		t.Fatalf("tenantclient.New: %v", err)
	}

	granter, err := devicekey.New(devicekey.Options{
		Binder:      store,
		Audience:    audience,
		TTL:         time.Hour,
		ExecCommand: execCommand,
		ExecEnvName: execEnvName,
	})
	if err != nil {
		t.Fatalf("devicekey.New: %v", err)
	}

	typed, err := kubernetes.NewForConfig(tenantCfg)
	if err != nil {
		t.Fatalf("tenant clientset: %v", err)
	}
	saName := tenantclient.SAName(did)
	if _, err := typed.CoreV1().ServiceAccounts(tenantclient.Namespace).Create(ctx,
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName}},
		metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("fixture: create ServiceAccount %s/%s: %v", tenantclient.Namespace, saName, err)
	}
	t.Logf("JOINT 0 ok -- fixture ServiceAccount %s/%s exists", tenantclient.Namespace, saName)

	rendered, err := granter.Grant(ctx, kcpclient.GrantInput{
		Caller:  gateway.Caller(did),
		Cluster: cluster,
		Server:  "https://kcp.live.test:6443/clusters/" + kcpclient.MountPath(did, cluster),
	})
	if err != nil {
		t.Fatalf("JOINT 1 FAILED -- devicekey.Grant: %v\n"+
			"  This is the grant half: generate the did:key, write the binding row to kcp "+
			"via tenantclient.Record, sign the credential, render the kubeconfig.", err)
	}
	t.Logf("JOINT 1 ok -- granted, kubeconfig is %d bytes", len(rendered))

	credential := credentialFromKubeconfig(t, rendered, execEnvName)
	t.Logf("JOINT 2 ok -- read %d-byte credential out of the rendered exec.env %q",
		len(credential), execEnvName)

	binding, err := store.Resolve(ctx, mint.DeviceCredential{Token: credential})
	if err != nil {
		t.Fatalf("JOINT 3/4/5 FAILED -- tenantclient.Resolve: %v\n"+
			"  Sub-joints: (3) verify the did:key credential and read the binding row from "+
			"%s ns=%s; (4) read the Cluster CR at %s; (5) read the ServiceAccount %s/%s LIVE.",
			err, registryWorkspace, registryNamespace, kcpclient.HomePath(did, cluster),
			tenantclient.Namespace, tenantclient.SAName(did))
	}
	t.Logf("JOINT 3 ok -- did:key verified locally and the row resolved")
	t.Logf("JOINT 4 ok -- Cluster CR read: instance=%q running=%v", binding.Instance, binding.Running)
	t.Logf("JOINT 5 ok -- ServiceAccount read LIVE: exists=%v", binding.SAExists)
	if binding.DID != did || binding.Cluster != cluster || binding.SA != saName {
		t.Fatalf("Resolve bound the wrong identity: got (did=%q cluster=%q sa=%q), want (%q, %q, %q)",
			binding.DID, binding.Cluster, binding.SA, did, cluster, saName)
	}
	if !binding.SAExists {
		t.Fatalf("Resolve reported SAExists=false for a ServiceAccount this test just created")
	}
	if binding.Instance == "" {
		t.Fatalf("status.instance is EMPTY on the Cluster CR at %s: mint.Mint.Issue refuses "+
			"this binding (mint.go:170) and would report ErrBindingUnknown for a healthy "+
			"cluster", kcpclient.HomePath(did, cluster))
	}

	token, expires, err := store.Issue(ctx, binding, 15*time.Minute)
	if err != nil {
		t.Fatalf("JOINT 6 FAILED -- tenantclient.Issue: %v", err)
	}
	if !expires.After(time.Now()) {
		t.Fatalf("JOINT 6 FAILED -- the apiserver returned an expiry in the past: %s", expires)
	}
	t.Logf("JOINT 6 ok -- TokenRequest issued %d chars, expires %s (in %s)",
		len(token), expires.UTC().Format(time.RFC3339), time.Until(expires).Round(time.Second))

	m := mint.New(store, mint.Options{TTL: 15 * time.Minute})
	exec, err := m.Issue(ctx, mint.DeviceCredential{Token: credential}, time.Now())
	if err != nil {
		t.Fatalf("JOINT 7 FAILED -- mint.Mint.Issue: %v", err)
	}
	renewAt, err := time.Parse(time.RFC3339, exec.Status.ExpirationTimestamp)
	if err != nil {
		t.Fatalf("JOINT 7 FAILED -- expirationTimestamp %q is not RFC3339: %v",
			exec.Status.ExpirationTimestamp, err)
	}
	if !renewAt.After(time.Now()) {
		t.Fatalf("JOINT 7 FAILED -- client-go would never renew: expirationTimestamp %s is "+
			"already past, so credsExpired() stays false and the FIRST token is reused for "+
			"the life of the process", renewAt)
	}
	if renewAt.After(expires) {
		t.Fatalf("JOINT 7 FAILED -- mint expires the credential at %s, AFTER the token's own "+
			"expiry %s: client-go would present a dead token", renewAt, expires)
	}
	t.Logf("JOINT 7 ok -- ExecCredential kind=%s renews at %s, %s before the token dies",
		exec.Kind, renewAt.UTC().Format(time.RFC3339), expires.Sub(renewAt))

	asUser := rest.CopyConfig(tenantCfg)
	asUser.CertData, asUser.CertFile, asUser.KeyData, asUser.KeyFile = nil, "", nil, ""
	asUser.Username, asUser.Password = "", ""
	asUser.BearerToken, asUser.BearerTokenFile = exec.Status.Token, ""
	asUserClient, err := kubernetes.NewForConfig(asUser)
	if err != nil {
		t.Fatalf("JOINT 8 FAILED -- client as the minted token: %v", err)
	}
	review, err := asUserClient.AuthenticationV1().SelfSubjectReviews().
		Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("JOINT 8 FAILED -- the minted token was REJECTED by the tenant apiserver: %v\n"+
			"  A token that mints but does not authenticate is ADR 0020's documented failure "+
			"mode (a custom audience is issued verbatim and then 401s through the mount). "+
			"This token was requested with `audiences: []`.", err)
	}
	t.Logf("JOINT 8 ok -- the minted token AUTHENTICATED as %q",
		review.Status.UserInfo.Username)

	if err := typed.CoreV1().ServiceAccounts(tenantclient.Namespace).
		Delete(ctx, saName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("JOINT 9 FAILED -- deleting the ServiceAccount: %v", err)
	}
	afterDelete, err := store.Resolve(ctx, mint.DeviceCredential{Token: credential})
	if err != nil {
		t.Fatalf("JOINT 9 FAILED -- Resolve errored after the SA was deleted: %v\n"+
			"  A deleted ServiceAccount is a REVOCATION, not an outage; it must report "+
			"SAExists=false on the success path.", err)
	}
	if afterDelete.SAExists {
		t.Fatalf("JOINT 9 FAILED -- Resolve still reports SAExists=true after the "+
			"ServiceAccount %s/%s was deleted. ADR 0024's whole mechanism is this read; a "+
			"stale answer here is a revoked identity that keeps minting for as long as the "+
			"process lives.", tenantclient.Namespace, saName)
	}
	fresh := mint.New(store, mint.Options{TTL: 15 * time.Minute})
	if _, err := fresh.Issue(ctx, mint.DeviceCredential{Token: credential}, time.Now()); err != mint.ErrServiceAccountGone {
		t.Fatalf("JOINT 9 FAILED -- mint returned %v, want mint.ErrServiceAccountGone.\n"+
			"  A revoked identity that mints anything is the failure ADR 0024 exists to "+
			"prevent.", err)
	}
	t.Logf("JOINT 9 ok -- revocation observed live; the mint failed closed with " +
		"ErrServiceAccountGone and issued nothing")

	priv, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		t.Fatalf("JOINT 10 FAILED -- generate a did:key: %v", err)
	}
	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatalf("JOINT 10 FAILED -- public half: %v", err)
	}
	unbound, err := auth.SignServiceAuth(syntax.DID(pub.DIDKey()), audience, time.Hour, nil, priv)
	if err != nil {
		t.Fatalf("JOINT 10 FAILED -- sign an unbound credential: %v", err)
	}
	if _, err := store.Resolve(ctx, mint.DeviceCredential{Token: unbound}); !errors.Is(err, mint.ErrBindingUnknown) {
		t.Fatalf("JOINT 10 FAILED -- an UNBOUND credential resolved to %v, want "+
			"mint.ErrBindingUnknown.\n"+
			"  The did:key %s was never written to %s ns=%s, so nothing may be minted for "+
			"it. Resolving it means the binding row is not the authority.",
			err, pub.DIDKey(), registryWorkspace, registryNamespace)
	}
	t.Logf("JOINT 10 ok -- an unbound did:key (never recorded) resolved to " +
		"ErrBindingUnknown, so the row really is the authority")

	t.Logf("THE WHOLE RENEWAL PATH RAN: grant -> kubeconfig -> resolve -> issue -> mint -> "+
		"AUTHENTICATED TOKEN -> revoked. did=%s cluster=%s home=%s",
		did, cluster, kcpclient.HomePath(did, cluster))
}

func liveConfig(t *testing.T, path string) *rest.Config {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("kubeconfig %s: %v", path, err)
	}
	if i := strings.Index(cfg.Host, "/clusters/"); i >= 0 {
		t.Logf("stripped %q from the kubeconfig server", cfg.Host[i:])
		cfg.Host = cfg.Host[:i]
	}
	return cfg
}

func credentialFromKubeconfig(t *testing.T, rendered, envName string) string {
	t.Helper()
	parsed, err := clientcmd.Load([]byte(rendered))
	if err != nil {
		t.Fatalf("JOINT 2 FAILED -- the kubeconfig devicekey.Grant rendered does not parse: %v\n%s",
			err, rendered)
	}
	for _, authInfo := range parsed.AuthInfos {
		if authInfo.Exec == nil {
			continue
		}
		for _, e := range authInfo.Exec.Env {
			if e.Name == envName && e.Value != "" {
				return e.Value
			}
		}
	}
	t.Fatalf("JOINT 2 FAILED -- no exec.env %q with a value in the rendered kubeconfig:\n%s",
		envName, rendered)
	return ""
}
