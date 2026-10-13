package nodemarket

import (
	"context"
	"errors"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

func seedClaim(claims *fakeClaims, phase v1alpha1.NodeClaimPhase, grant *v1alpha1.NodeClaimGrant) v1alpha1.NodeClaim {
	claim := v1alpha1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: testNodeName},
		Spec:       v1alpha1.NodeClaimSpec{Cluster: "liveweb", NodeName: testNodeName},
		Status:     v1alpha1.NodeClaimStatus{Phase: phase, Grant: grant},
	}
	claims.mu.Lock()
	defer claims.mu.Unlock()
	if claims.claims == nil {
		claims.claims = map[string]v1alpha1.NodeClaim{}
	}
	claims.claims[claims.key(testWorkspace, testNodeName)] = claim
	return claim
}

func TestAnAdoptedJoinedHostRewritesNoSecretAndLogsInFromTheGrant(t *testing.T) {
	for _, phase := range []v1alpha1.NodeClaimPhase{v1alpha1.NodeClaimRunning} {
		t.Run(string(phase), func(t *testing.T) {
			opts, claims, secrets, links, _ := secretOptions(t)
			links.listen(testWorkspace, "liveweb-apiserver", "ticket")
			links.listen(testWorkspace, "liveweb-openbao", "openbao-ticket")
			grant := v1alpha1.NodeClaimGrant{Issuer: "https://bidder.example.com", Subject: "vm:abc",
				Audience: "socialweb.computer/node/liveweb/" + testNodeName}
			claim := seedClaim(claims, phase, &grant)
			host, err := New(opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := host.Adopt(claim); err != nil {
				t.Fatalf("Adopt: %v", err)
			}
			for range 2 {
				if err := host.SyncSecrets(context.Background()); err != nil {
					t.Fatalf("SyncSecrets: %v", err)
				}
			}
			if len(secrets.policies) != 0 || len(secrets.secrets) != 0 || claims.writes != 0 {
				t.Fatalf("an adopted %s claim wrote policies %v, secrets %d and claims %d; want none: "+
					"the guest already fetched its secret and the claim is the previous process's",
					phase, secrets.policies, len(secrets.secrets), claims.writes)
			}
			if secrets.ensures != 1 || secrets.logins[testNodeName] != grant {
				t.Fatalf("two passes over the adopted grant ensured the login %d times bound to %+v, "+
					"want once to %+v", secrets.ensures, secrets.logins[testNodeName], grant)
			}
		})
	}
}

func TestAnAdoptedClaimTheGuestHasNotFetchedYetGetsItsSecretWritten(t *testing.T) {
	for _, phase := range []v1alpha1.NodeClaimPhase{v1alpha1.NodeClaimProvisioning, v1alpha1.NodeClaimJoining} {
		t.Run(string(phase), func(t *testing.T) { adoptedClaimGetsItsSecretWritten(t, phase) })
	}
}

func adoptedClaimGetsItsSecretWritten(t *testing.T, phase v1alpha1.NodeClaimPhase) {
	opts, claims, secrets, links, _ := secretOptions(t)
	links.listen(testWorkspace, "liveweb-apiserver", "ticket")
	links.listen(testWorkspace, "liveweb-openbao", "openbao-ticket")
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		t.Fatalf("creating the data dir: %v", err)
	}
	if _, err := MintOrReadNodePassword(NodePasswordPath(opts.DataDir)); err != nil {
		t.Fatalf("minting the node password: %v", err)
	}
	claim := seedClaim(claims, phase, nil)
	host, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Adopt(claim); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if err := host.SyncSecrets(context.Background()); err != nil {
		t.Fatalf("SyncSecrets: %v", err)
	}
	if _, ok := secrets.secret(testNodeName); !ok || len(secrets.policies) != 0 || claims.writes != 0 {
		t.Fatalf("an adopted %s claim: secret written %v, policies %v, claim writes %d; want the secret "+
			"written (the previous process may have held it back, and Joining only says the guest has its "+
			"contract secrets: it reads its join secret from the cluster OpenBao after that, and the "+
			"instance token in it rotated with the restart) and nothing else", phase, ok,
			secrets.policies, claims.writes)
	}
}

func TestAnEndedOrDeletingClaimIsNotAdopted(t *testing.T) {
	now := metav1.Now()
	for name, mutate := range map[string]func(*v1alpha1.NodeClaim){
		"Failed":   func(c *v1alpha1.NodeClaim) { c.Status.Phase = v1alpha1.NodeClaimFailed },
		"Released": func(c *v1alpha1.NodeClaim) { c.Status.Phase = v1alpha1.NodeClaimReleased },
		"deleting": func(c *v1alpha1.NodeClaim) { c.DeletionTimestamp = &now },
		"other":    func(c *v1alpha1.NodeClaim) { c.Spec.Cluster = "otherweb" },
	} {
		t.Run(name, func(t *testing.T) {
			opts, claims, _, _, _ := secretOptions(t)
			claim := seedClaim(claims, v1alpha1.NodeClaimRunning, nil)
			mutate(&claim)
			host, err := New(opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := host.Adopt(claim); !errors.Is(err, ErrNotAdoptable) {
				t.Fatalf("Adopt(%s) = %v, want ErrNotAdoptable", name, err)
			}
		})
	}
}
