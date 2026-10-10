package main

import (
	"context"
	"os"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/livegate"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantclient"
)

const mintIdentity = "system:serviceaccount:users:gwmint"

func TestTheMintCredentialAgainstRealInfrastructure(t *testing.T) {
	livegate.Short(t, "this test mints against real tenant infrastructure and -short was asked for: a skip is NOT a pass; drop -short, or set SOCIALWEB_REQUIRE_LIVE=1 to fail instead")
	dir := os.Getenv("SOCIALWEB_LIVE_MINT_KUBECONFIGS")
	cluster := os.Getenv("SOCIALWEB_LIVE_CLUSTER")
	did := os.Getenv("SOCIALWEB_LIVE_DID")
	grantedSA := os.Getenv("SOCIALWEB_LIVE_GRANTED_SA")
	for _, env := range []struct{ name, value string }{
		{"SOCIALWEB_LIVE_MINT_KUBECONFIGS", dir},
		{"SOCIALWEB_LIVE_CLUSTER", cluster},
		{"SOCIALWEB_LIVE_DID", did},
		{"SOCIALWEB_LIVE_GRANTED_SA", grantedSA},
	} {
		if env.value == "" {
			livegate.Require(t, "NOT RUN: %s is unset.\n"+
				"  This asserts that the credential SOCIALWEB_GATEWAY_MINT_KUBECONFIGS holds "+
				"authenticates as %s and is SCOPED, which no temp-directory test can reach.\n"+
				"  It needs a directory containing a kubeconfig named for the cluster label, "+
				"whose identity is that cluster's `gwmint` ServiceAccount.", env.name, mintIdentity)
		}
	}

	ctx := context.Background()
	configs := mintCredentials(dir)

	cfg, err := configs(did, cluster)
	if err != nil {
		t.Fatalf("MintConfig(%q, %q): %v", did, cluster, err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("client for the mint credential: %v", err)
	}
	review, err := clientset.AuthenticationV1().SelfSubjectReviews().
		Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("the mint credential was REJECTED by the tenant apiserver: %v\n"+
			"  A credential that loads but does not authenticate is the failure this test "+
			"exists for: the mint would answer 401 on every renewal, about fifteen minutes "+
			"after handing out a kubeconfig that looked fine.", err)
	}
	if got := review.Status.UserInfo.Username; got != mintIdentity {
		t.Fatalf("the mint credential authenticates as %q, want %q.\n"+
			"  THIS IS THE ASSERTION THAT DECIDES WHETHER THE DIRECTORY HOLDS A SCOPED "+
			"CREDENTIAL OR AN ADMIN ONE: mintCredentials cannot look inside the kubeconfig, "+
			"so a kubeconfig naming the tenant admin is accepted there and read as correct. "+
			"Only the apiserver's own answer separates them.", got, mintIdentity)
	}
	t.Logf("ok -- the mint credential AUTHENTICATED as %q", review.Status.UserInfo.Username)

	seconds := int64(900)
	request := func(sa string) (*authenticationv1.TokenRequest, error) {
		return clientset.CoreV1().ServiceAccounts(tenantclient.Namespace).
			CreateToken(ctx, sa, &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences:         []string{},
					ExpirationSeconds: &seconds,
				},
			}, metav1.CreateOptions{})
	}

	token, err := request(grantedSA)
	if err != nil {
		t.Fatalf("the credential could NOT mint for %s/%s, the ServiceAccount the grant's "+
			"resourceNames names: %v\n"+
			"  Either the applied grant does not cover this DID, or the credential is not "+
			"`gwmint`. Both are credential problems, not token problems.",
			tenantclient.Namespace, grantedSA, err)
	}
	if token.Status.Token == "" || token.Status.ExpirationTimestamp.IsZero() {
		t.Fatalf("the TokenRequest returned no token or no expiry: %+v", token.Status)
	}
	t.Logf("ok -- minted for the granted ServiceAccount %s/%s: %d chars, expires %s",
		tenantclient.Namespace, grantedSA, len(token.Status.Token),
		token.Status.ExpirationTimestamp.UTC().Format(time.RFC3339))

	uncovered := tenantclient.SAName("did:plc:not-covered-by-the-grant")
	if uncovered == grantedSA {
		t.Fatalf("the fixture is wrong: %q and the granted name are the same", uncovered)
	}
	if _, err := request(uncovered); err == nil {
		t.Fatalf("the credential minted for %s/%s, which the applied grant does NOT name.\n"+
			"  An unscoped credential is every DID's identity in this namespace: it would "+
			"mint for any account that has a ServiceAccount here.", tenantclient.Namespace, uncovered)
	} else if !apierrors.IsForbidden(err) {
		t.Fatalf("the request for %s/%s failed with %v, want a 403 Forbidden.\n"+
			"  Only a 403 from the authorizer shows that the grant BOUNDED this credential; "+
			"any other error means the negative proved nothing.",
			tenantclient.Namespace, uncovered, err)
	}
	t.Logf("ok -- and REFUSED (403) for %s/%s, so the credential is bounded to the names "+
		"the grant lists", tenantclient.Namespace, uncovered)

	if cfg, err := configs(did, "no-such-cluster-label"); err == nil || cfg != nil {
		t.Fatalf("an unknown cluster returned (%v, %v) against the real credential directory, "+
			"so the mint would fall back to another cluster's identity", cfg, err)
	}
	t.Logf("ok -- an unknown cluster label failed closed against the real directory")
}
