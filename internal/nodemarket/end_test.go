package nodemarket

import (
	"context"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

func (f *fakeClaims) setContract(workspace, name string, contract v1alpha1.NodeClaimContract) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claim := f.claims[f.key(workspace, name)]
	claim.Status.Contract = contract
	f.claims[f.key(workspace, name)] = claim
}

func TestAnEndWithAnRFPAndNoAcceptIsTheMarketDeclining(t *testing.T) {
	for _, tc := range []struct {
		name     string
		phase    v1alpha1.NodeClaimPhase
		contract v1alpha1.NodeClaimContract
		declined bool
	}{
		{"no bids", v1alpha1.NodeClaimFailed,
			v1alpha1.NodeClaimContract{VMUri: "at://o/vm/1", RFPUri: "at://o/rfp/1"}, true},
		{"failed after a bid was accepted", v1alpha1.NodeClaimFailed,
			v1alpha1.NodeClaimContract{VMUri: "at://o/vm/1", RFPUri: "at://o/rfp/1", AcceptUri: "at://o/accept/1"}, false},
		{"failed before any rfp", v1alpha1.NodeClaimFailed, v1alpha1.NodeClaimContract{}, false},
		{"released", v1alpha1.NodeClaimReleased,
			v1alpha1.NodeClaimContract{VMUri: "at://o/vm/1", RFPUri: "at://o/rfp/1"}, false},
		{"still bidding", v1alpha1.NodeClaimBidding,
			v1alpha1.NodeClaimContract{VMUri: "at://o/vm/1", RFPUri: "at://o/rfp/1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim := &v1alpha1.NodeClaim{Status: v1alpha1.NodeClaimStatus{Phase: tc.phase, Contract: tc.contract}}
			if got := MarketDeclined(claim); got != tc.declined {
				t.Fatalf("MarketDeclined = %v, want %v. The door records an rfpUri when it asks the "+
					"market and an acceptUri when a bid is taken, so a Failed claim with the first and "+
					"not the second is an RFP no bidder took -- the end that, given up and asked again, "+
					"posts a public RFP every pass", got, tc.declined)
			}
		})
	}
}

func TestTheHostReportsADeclinedEndWithTheDoorsWords(t *testing.T) {
	claims := &fakeClaims{}
	host, err := New(testOptions(t, claims))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if end := host.End(); end != (End{}) {
		t.Fatalf("a pending claim reported the end %+v", end)
	}
	claims.setContract(testWorkspace, testNodeName, v1alpha1.NodeClaimContract{RFPUri: "at://o/rfp/1"})
	claims.setPhase(testWorkspace, testNodeName, v1alpha1.NodeClaimFailed, "no bids arrived for this node")
	end := host.End()
	if !end.Declined || end.Message != "no bids arrived for this node" {
		t.Fatalf("End = %+v, want a declined end carrying the door's message", end)
	}
	if got := host.Terminal(); got != end.Message {
		t.Fatalf("Terminal = %q and End.Message = %q: two readings of one claim disagree", got, end.Message)
	}
}

func TestAClaimDeletedUnderAHeldNodeIsAnEnd(t *testing.T) {
	claims := &fakeClaims{}
	host, err := New(testOptions(t, claims))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := claims.DeleteNodeClaim(context.Background(), testWorkspace, testNodeName); err != nil {
		t.Fatalf("DeleteNodeClaim: %v", err)
	}
	if claim, _ := claims.ReadNodeClaim(context.Background(), testWorkspace, testNodeName); claim != nil {
		t.Fatalf("the fake kept the claim after its delete: %+v", claim.Status)
	}
	end := host.End()
	if end.Message == "" || end.Declined {
		t.Fatalf("End = %+v after the claim of a provisioned node was deleted, want an end that is "+
			"not a declined RFP. Provision writes the claim before the host is held, so a held host "+
			"whose claim reads NotFound was released by someone else, and a host that reports no end "+
			"stays counted forever: the cluster never asks the market again", end)
	}
}

func TestAClaimTheDoorMarkedLostIsAnEndThatIsNotTheMarketDeclining(t *testing.T) {
	claims := &fakeClaims{}
	host, err := New(testOptions(t, claims))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := host.Provision(context.Background()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	claims.setContract(testWorkspace, testNodeName, v1alpha1.NodeClaimContract{RFPUri: "at://o/rfp/1"})
	claims.setPhase(testWorkspace, testNodeName, v1alpha1.NodeClaimLost, "the bidder reported the guest terminated (guest_poweroff)")
	end := host.End()
	if !end.Lost || end.Declined || end.Message != "the bidder reported the guest terminated (guest_poweroff)" {
		t.Fatalf("End = %+v, want a Lost end carrying the door's message and no market back-off", end)
	}
	if Adoptable(v1alpha1.NodeClaim{Status: v1alpha1.NodeClaimStatus{Phase: v1alpha1.NodeClaimLost}}) {
		t.Fatal("a Lost claim is adoptable; its guest is gone and adopting it holds a node nothing runs")
	}
}
