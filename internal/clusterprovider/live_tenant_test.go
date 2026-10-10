//go:build linux

package clusterprovider

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
)

func TestLiveBootstrapRunsAgainstARealTenantCluster(t *testing.T) {
	livegate.Short(t, "this test runs the bootstrap against a real tenant cluster and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	kubeconfig := os.Getenv("CLUSTERPROVIDER_LIVE_TENANT_KUBECONFIG")
	if kubeconfig == "" {
		livegate.Require(t, "set CLUSTERPROVIDER_LIVE_TENANT_KUBECONFIG to a tenant cluster admin kubeconfig")
	}
	grant := os.Getenv("CLUSTERPROVIDER_LIVE_TENANT_GRANT")
	if grant == "" {
		grant = "../../deploy/tenant-grant.yaml"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("reading %s: %v", kubeconfig, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	existing, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("a client for the tenant cluster: %v", err)
	}
	_, beforeErr := existing.CoreV1().Secrets(kcpclient.TenantNamespace).Get(ctx, mintSecretName, metav1.GetOptions{})
	preExisting := beforeErr == nil
	t.Logf("the mint Secret %s/%s exists before this run: %v", kcpclient.TenantNamespace, mintSecretName, preExisting)

	mintDir := t.TempDir()
	p, err := New(Options{
		Registry: &fakeClusters{},
		Decider:  decider(t, &fakeHost{}),
		Tenant: func(context.Context, Ref, *v1alpha1.Cluster) (*rest.Config, error) {
			return cfg, nil
		},
		GrantPath: grant,
		MintDir:   mintDir,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const cluster = "liveweb"
	ref := Ref{Name: cluster, DID: "did:plc:renewallive0000000000000000"}
	if err := p.Bootstrap(ctx, ref, &v1alpha1.Cluster{}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	path := filepath.Join(mintDir, cluster)
	got, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("the gateway would not be able to read %s: %v", path, err)
	}
	if got.BearerToken == "" {
		t.Fatal("the mint credential carries no token, which is the one thing it is for")
	}
	if got.BearerToken != strings.TrimSpace(got.BearerToken) {
		t.Error("the token has surrounding whitespace: a Secret's data is base64 and this is " +
			"the one place a trailing newline silently becomes part of a bearer token")
	}
	if len(got.TLSClientConfig.CAData) == 0 {
		t.Error("the mint credential carries no CA")
	}
	t.Logf("mint credential: %s host=%s token=%d bytes ca=%d bytes",
		path, got.Host, len(got.BearerToken), len(got.TLSClientConfig.CAData))

	mc, err := p.MintConfig(cluster)
	if err != nil {
		t.Fatalf("MintConfig after a bootstrap: %v", err)
	}
	if mc.BearerToken != got.BearerToken {
		t.Error("the recorded credential and the written file disagree, so a caller holding " +
			"the Provider and the gateway binary would mint as different identities")
	}
	if _, err := p.MintConfig("no-such-cluster"); err == nil {
		t.Error("MintConfig answered for a cluster that was never bootstrapped; the fail-closed " +
			"property is what keeps a miss from falling back to a wider credential")
	}

	minted, err := kubernetes.NewForConfig(got)
	if err != nil {
		t.Fatalf("a client from the mint credential: %v", err)
	}
	who, err := minted.AuthenticationV1().SelfSubjectReviews().Create(ctx,
		&authv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("SelfSubjectReview with the mint credential: %v", err)
	}
	t.Logf("the mint credential authenticates as: %s groups=%v",
		who.Status.UserInfo.Username, who.Status.UserInfo.Groups)

	allowed := kcpclient.ServiceAccountName(ref.DID)
	other := kcpclient.ServiceAccountName("did:plc:notthisowner000000000000000")
	if _, err := minted.CoreV1().ServiceAccounts(kcpclient.TenantNamespace).Get(ctx, allowed, metav1.GetOptions{}); err != nil {
		t.Errorf("the mint credential cannot read the ServiceAccount its own grant names (%s): %v", allowed, err)
	}
	if _, err := minted.CoreV1().ServiceAccounts(kcpclient.TenantNamespace).Get(ctx, other, metav1.GetOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("the mint credential read %s and should not have: resourceNames scopes this "+
			"grant to ONE name, and a credential that reads past it is wider than ADR 0020 "+
			"says it is. err=%v", other, err)
	}

	if preExisting {
		t.Logf("LEFT IN PLACE: the mint Secret predates this run, so it belongs to whatever "+
			"created it -- %s/%s", kcpclient.TenantNamespace, mintSecretName)
	} else if err := existing.CoreV1().Secrets(kcpclient.TenantNamespace).Delete(ctx, mintSecretName, metav1.DeleteOptions{}); err != nil {
		t.Errorf("this run created %s/%s and could not remove it, so it left a LIVE, "+
			"NON-EXPIRING credential behind: %v", kcpclient.TenantNamespace, mintSecretName, err)
	} else {
		t.Logf("REMOVED: this run created %s/%s and deleted it, because a filled legacy token "+
			"Secret is a standing credential and a test must not leave one lying in a shared "+
			"cluster. The PRODUCTION bootstrap leaves it -- that Secret IS the credential the "+
			"gateway mints with.", kcpclient.TenantNamespace, mintSecretName)
	}
}
