package vaultlayout

import (
	"strings"
	"testing"
)

func TestCertIdentityNamesAreValidated(t *testing.T) {
	for _, ok := range []string{"node-a", "node1", "a", "node-123", "0"} {
		if err := ValidateCertIdentity(ok); err != nil {
			t.Fatalf("ValidateCertIdentity(%q) refused a valid lower-case DNS label: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Node-A", "node/a", "-node", "node-", "node.a", strings.Repeat("a", 64)} {
		if err := ValidateCertIdentity(bad); err == nil {
			t.Fatalf("ValidateCertIdentity(%q) accepted a name that is not a lower-case DNS label, "+
				"so the certificate would bind to a role or read a path the caller did not write", bad)
		}
	}
}

func TestTheNodePolicyReadsExactlyItsOwnPath(t *testing.T) {
	hcl := NodeCertPolicyHCL("node-a")
	if !strings.Contains(hcl, NodeSecretPath("node-a")) {
		t.Fatalf("the node policy does not read its own path:\n%s", hcl)
	}
	if strings.Contains(hcl, "node-b") {
		t.Fatalf("the node policy reads another node's path:\n%s", hcl)
	}
	if !strings.Contains(hcl, `"read"`) {
		t.Fatalf("the node policy does not grant read:\n%s", hcl)
	}
	if got := NodeSecretPath("node-a"); got != "secret/data/k3s/node/node-a" {
		t.Fatalf("NodeSecretPath(node-a) is %q", got)
	}
	if NodeSecretPath("node-a") == NodeSecretPath("node-b") {
		t.Fatal("two nodes share one secret path")
	}
	if NodeCertRoleName("Node-A") != "node-node-a" {
		t.Fatalf("NodeCertRoleName lower-cases to %q, want the role OpenBao stores", NodeCertRoleName("Node-A"))
	}
	if NodePolicyName("Node-A") != "node-secret-node-a" {
		t.Fatalf("NodePolicyName lower-cases to %q, want the policy OpenBao stores", NodePolicyName("Node-A"))
	}
}

func TestTheCertIdentitiesDoNotCollideWithTheESORole(t *testing.T) {
	if NodeCertRoleName("eso-openbao") == RoleESO {
		t.Fatal("a node named eso-openbao would take the ESO role, so one certificate could read " +
			"the other identity's paths")
	}
	if NodePolicyName("eso-openbao") == PolicyESO {
		t.Fatal("a node named eso-openbao would take the ESO policy")
	}
}

func TestTheESOPolicyGrantsItsSecretAndTheTwoSelfTokenCallsOnly(t *testing.T) {
	want := `path "auth/token/lookup-self" {
  capabilities = ["read"]
}
path "secret/data/k3s/eso-demo" {
  capabilities = ["read"]
}
path "auth/token/revoke-self" {
  capabilities = ["update"]
}
`
	if got := ESOCertPolicyHCL(); got != want {
		t.Fatalf("the ESO policy is:\n%s\nwant exactly:\n%s\nthe provider calls auth/token/lookup-self to "+
			"validate the store, auth/token/revoke-self when it closes the client, and reads only the "+
			"eso-demo path, so any other path or capability is broader than the provider needs", got, want)
	}
}

func TestTheLayoutStillValidatesWithTheCertHelpers(t *testing.T) {
	if err := Validate(Default()); err != nil {
		t.Fatalf("Validate(Default()): %v", err)
	}
}
