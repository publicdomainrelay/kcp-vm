package clusterprovider

import (
	"path/filepath"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
)

func TestTheDoorFindsTheClusterOpenBaoCABesideTheTokenTheClaimNames(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "pdr-key-cluster")
	derived := filepath.Join(filepath.Dir(TokenPath(dataDir)), "openbao", "tls", "ca.crt")
	if want := clusterstack.OpenBaoTLSCAPath(dataDir); derived != want {
		t.Fatalf("the door derives the cluster OpenBao CA as %s from the claim's spec.tokenFile, but "+
			"the provider writes it at %s; the door hands that CA to every market guest, so a "+
			"guest would be given a file that is not its OpenBao's CA and its login would fail TLS",
			derived, want)
	}
}
