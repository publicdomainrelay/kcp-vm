package v1alpha1

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func ptr(v int64) *int64 { return &v }

func TestNodeExpiryDistinguishesUnlimitedFromAbsent(t *testing.T) {
	cases := []struct {
		name      string
		expiry    *int64
		wantKey   bool
		wantValue float64
	}{
		{"absent means no producer, and the gate refuses", nil, false, 0},
		{"zero means UNLIMITED, and the gate proceeds", ptr(0), true, 0},
		{"a deadline is an absolute unix second", ptr(1758500000), true, 1758500000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []any{
				&ClusterStatus{NodeExpiry: tc.expiry},
				&NodeStatus{NodeExpiry: tc.expiry},
			} {
				raw, err := json.Marshal(status)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var got map[string]any
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				value, present := got["nodeExpiry"]
				if present != tc.wantKey {
					t.Fatalf("%T: nodeExpiry present=%v, want %v -- a plain int64 would make "+
						"UNLIMITED indistinguishable from ABSENT, and the teardown gate refuses "+
						"when the value is unknown, so every unlimited cluster would be undeletable",
						status, present, tc.wantKey)
				}
				if present && value != tc.wantValue {
					t.Fatalf("%T: nodeExpiry=%v, want %v", status, value, tc.wantValue)
				}
			}
		})
	}
}

func TestMountContractFieldNamesAreNotGoIdiomatic(t *testing.T) {
	raw, err := json.Marshal(&ClusterStatus{URL: "https://example.invalid", Phase: ClusterReady})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["URL"]; !ok {
		t.Fatalf("ClusterStatus must serialize the field as URL (capitals); kcp reads it by "+
			"that name and a rename to idiomatic Go casing silently breaks every mount. got: %v", got)
	}
	if _, ok := got["phase"]; !ok {
		t.Fatalf("ClusterStatus must serialize phase for kcp's Ready check. got: %v", got)
	}
}

func TestOnlyTheLiteralReadySatisfiesAMount(t *testing.T) {
	for _, p := range []ClusterPhase{
		ClusterInitializing, ClusterUnavailable, ClusterDraining,
	} {
		if p == ClusterReady {
			t.Fatalf("%q must not be the Ready literal", p)
		}
	}
	if ClusterReady != "Ready" {
		t.Fatalf("kcp compares against the literal %q, not %q", "Ready", ClusterReady)
	}
	phases := map[ClusterPhase]bool{
		ClusterInitializing: true, ClusterReady: true,
		ClusterUnavailable: true, ClusterDraining: true,
	}
	if len(phases) != 4 {
		t.Fatalf("two phases collided: %v", phases)
	}
}

func TestSchemeRoundTripsEveryType(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	for _, want := range []struct {
		kind string
		obj  runtime.Object
	}{
		{"Cluster", &Cluster{}},
		{"ClusterList", &ClusterList{}},
		{"Node", &Node{}},
		{"NodeList", &NodeList{}},
		{"NodeClaim", &NodeClaim{}},
		{"NodeClaimList", &NodeClaimList{}},
		{"AtprotoIdentity", &AtprotoIdentity{}},
		{"AtprotoIdentityList", &AtprotoIdentityList{}},
	} {
		gvks, _, err := scheme.ObjectKinds(want.obj)
		if err != nil {
			t.Fatalf("ObjectKinds(%s): %v", want.kind, err)
		}
		if len(gvks) == 0 || gvks[0].Kind != want.kind {
			t.Fatalf("kind round-trip: got %v, want %s", gvks, want.kind)
		}
		if gvks[0].Group != GroupName {
			t.Fatalf("%s registered under group %q, want %q", want.kind, gvks[0].Group, GroupName)
		}
	}
}

func TestDeepCopyDoesNotAliasNodeExpiryOrConditions(t *testing.T) {
	orig := &Cluster{
		Status: ClusterStatus{
			NodeExpiry: ptr(42),
			Conditions: []metav1.Condition{{Type: ConditionTeardownBlocked, Status: metav1.ConditionTrue}},
		},
	}
	dup := orig.DeepCopy()
	*dup.Status.NodeExpiry = 99
	dup.Status.Conditions[0].Type = "mutated"
	if *orig.Status.NodeExpiry != 42 {
		t.Fatalf("DeepCopy aliased nodeExpiry: original changed to %d", *orig.Status.NodeExpiry)
	}
	if orig.Status.Conditions[0].Type != ConditionTeardownBlocked {
		t.Fatalf("DeepCopy aliased conditions: original changed to %q", orig.Status.Conditions[0].Type)
	}
}

func TestNodeClaimFieldNamesAreTheDoorsWireContract(t *testing.T) {
	raw, err := json.Marshal(&NodeClaim{
		Spec: NodeClaimSpec{
			Cluster: "web", NodeName: "web-node1", OwnerDid: "did:web:example.com",
			ApiserverPort: 24000, AdvertiseAddress: "100.127.255.254",
			TokenFile: "/state/token", NodePasswordFile: "/state/password",
			ServerCAFile: "/state/server-ca.crt", DenyBidderDids: []string{"did:plc:bad"},
		},
		Status: NodeClaimStatus{
			Phase: NodeClaimRunning, Fqdn: "node.example.invalid", Message: "joined",
			Contract: NodeClaimContract{
				VMUri: "at://x/1", RFPUri: "at://x/2", AcceptUri: "at://x/3",
				ReceiptUri: "at://x/4", ReceiptCid: "bafyrei", SubmitEventRef: "at://x/5",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec, _ := got["spec"].(map[string]any)
	for _, field := range []string{
		"cluster", "nodeName", "ownerDid", "apiserverPort", "advertiseAddress",
		"tokenFile", "nodePasswordFile", "serverCAFile", "denyBidderDids",
	} {
		if _, ok := spec[field]; !ok {
			t.Errorf("spec.%s is missing from the wire form. The door is a separate process that "+
				"reads these names literally, and a Go-idiomatic rename writes a claim it reads as "+
				"empty. got: %v", field, spec)
		}
	}
	status, _ := got["status"].(map[string]any)
	for _, field := range []string{"phase", "fqdn", "message"} {
		if _, ok := status[field]; !ok {
			t.Errorf("status.%s is missing from the wire form. got: %v", field, status)
		}
	}
	contract, _ := status["contract"].(map[string]any)
	for _, field := range []string{
		"vmUri", "rfpUri", "acceptUri", "receiptUri", "receiptCid", "submitEventRef",
	} {
		if _, ok := contract[field]; !ok {
			t.Errorf("status.contract.%s is missing from the wire form. got: %v", field, contract)
		}
	}
}

func TestNodeClaimPhasesAreTheDoorsLiterals(t *testing.T) {
	want := map[NodeClaimPhase]string{
		NodeClaimPending:      "Pending",
		NodeClaimBidding:      "Bidding",
		NodeClaimProvisioning: "Provisioning",
		NodeClaimJoining:      "Joining",
		NodeClaimRunning:      "Running",
		NodeClaimFailed:       "Failed",
		NodeClaimReleased:     "Released",
		NodeClaimLost:         "Lost",
		NodeClaimAdopting:     "Adopting",
	}
	if len(want) != 9 {
		t.Fatalf("two phases collided: %v", want)
	}
	for phase, literal := range want {
		if string(phase) != literal {
			t.Errorf("%q is not the literal the door writes (%q)", phase, literal)
		}
	}
	if FinalizerNodeClaim != "nodeclaim.socialweb.computer/release" {
		t.Fatalf("the finalizer is %q, and the door removes the one it names: a different string "+
			"is a claim the door never releases, which is a machine that is never destroyed",
			FinalizerNodeClaim)
	}
}

func TestDeepCopyDoesNotAliasTheDeniedBidderDids(t *testing.T) {
	orig := &NodeClaim{Spec: NodeClaimSpec{DenyBidderDids: []string{"did:plc:bad"}}}
	dup := orig.DeepCopy()
	dup.Spec.DenyBidderDids[0] = "did:plc:other"
	if orig.Spec.DenyBidderDids[0] != "did:plc:bad" {
		t.Fatalf("DeepCopy aliased denyBidderDids: original changed to %q",
			orig.Spec.DenyBidderDids[0])
	}
}

func TestNodeClaimResumeFieldsAreTheDoorsWireContract(t *testing.T) {
	exit := 0
	raw, err := json.Marshal(&NodeClaimStatus{
		Phase: NodeClaimLost,
		Contract: NodeClaimContract{
			AcceptPending: &ContractAcceptPending{BidUri: "at://b/bid/1"},
			Termination: &ContractTermination{
				Reason: "guest_poweroff", ExitCode: &exit, ObservedAt: "2026-10-12T00:00:00Z",
				Event: RecordRef{URI: "at://b/event/1", CID: "e"}, Payload: RecordRef{URI: "at://b/terminated/1", CID: "p"},
			},
		},
		Secrets: &NodeClaimSecrets{IngressDid: "did:key:z", KeyRef: "socialweb-door/claims/n/secrets-ingress", URL: "https://did-key-z.relay", ServedAt: "2026-10-12T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["phase"] != "Lost" {
		t.Errorf("phase = %v, want Lost", got["phase"])
	}
	secrets, _ := got["secrets"].(map[string]any)
	for _, field := range []string{"ingressDid", "keyRef", "url", "servedAt"} {
		if _, ok := secrets[field]; !ok {
			t.Errorf("status.secrets.%s is missing from the wire form. got: %v", field, secrets)
		}
	}
	contract, _ := got["contract"].(map[string]any)
	pending, _ := contract["acceptPending"].(map[string]any)
	if pending["bidUri"] != "at://b/bid/1" {
		t.Errorf("status.contract.acceptPending.bidUri missing: %v", contract)
	}
	termination, _ := contract["termination"].(map[string]any)
	for _, field := range []string{"reason", "exitCode", "observedAt", "event", "payload"} {
		if _, ok := termination[field]; !ok {
			t.Errorf("status.contract.termination.%s is missing from the wire form. got: %v", field, termination)
		}
	}
	dup := (&NodeClaimStatus{Contract: NodeClaimContract{Termination: &ContractTermination{ExitCode: &exit}}, Secrets: &NodeClaimSecrets{URL: "a"}})
	var copied NodeClaimStatus
	dup.DeepCopyInto(&copied)
	*copied.Contract.Termination.ExitCode = 9
	copied.Secrets.URL = "b"
	if *dup.Contract.Termination.ExitCode != 0 || dup.Secrets.URL != "a" {
		t.Fatalf("DeepCopy aliased the termination or secrets")
	}
}

func TestNodeClaimHostKeysAreTheDoorsWireContract(t *testing.T) {
	raw, err := json.Marshal(&NodeClaimStatus{
		Contract: NodeClaimContract{
			HostKeys: &ContractHostKeys{
				Keys: []ContractHostKey{{
					Type: "ssh-ed25519", PublicKey: "AAAAC3NzaC1lZDI1NTE5AAAAI", Fingerprint: "SHA256:x",
				}},
				ObservedAt: "2026-10-12T00:00:00Z",
				Event:      RecordRef{URI: "at://b/event/2", CID: "e"},
				Payload:    RecordRef{URI: "at://b/hostKeys/1", CID: "p"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	contract, _ := got["contract"].(map[string]any)
	hostKeys, _ := contract["hostKeys"].(map[string]any)
	for _, field := range []string{"keys", "observedAt", "event", "payload"} {
		if _, ok := hostKeys[field]; !ok {
			t.Errorf("status.contract.hostKeys.%s is missing from the wire form. got: %v", field, hostKeys)
		}
	}
	keys, _ := hostKeys["keys"].([]any)
	key, _ := keys[0].(map[string]any)
	for _, field := range []string{"type", "publicKey", "fingerprint"} {
		if _, ok := key[field]; !ok {
			t.Errorf("status.contract.hostKeys.keys[0].%s is missing from the wire form. got: %v", field, key)
		}
	}
	dup := &NodeClaimStatus{Contract: NodeClaimContract{HostKeys: &ContractHostKeys{Keys: []ContractHostKey{{Type: "a"}}}}}
	var copied NodeClaimStatus
	dup.DeepCopyInto(&copied)
	copied.Contract.HostKeys.Keys[0].Type = "b"
	if dup.Contract.HostKeys.Keys[0].Type != "a" {
		t.Fatalf("DeepCopy aliased the host keys")
	}
}
