package providermain

import (
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

func readyPhase(t *testing.T) v1alpha1.ClusterPhase {
	t.Helper()
	settled, err := revocation.WakeSettled(revocation.Effect{State: v1alpha1.IdentityRevoked})
	if err != nil {
		t.Fatalf("revocation.WakeSettled: %v", err)
	}
	phase, err := revocation.Ready(settled)
	if err != nil {
		t.Fatalf("revocation.Ready: %v", err)
	}
	return phase
}
