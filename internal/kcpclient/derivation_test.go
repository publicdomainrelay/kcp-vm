package kcpclient_test

import (
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
)

func TestTheDerivedNamesAreTheADRVector(t *testing.T) {
	for _, tc := range []struct {
		did  string
		want string
	}{
		{"did:plc:testaccount0000000000000000", "did-mruwiotqnrrtu5dfon2gcy3dn52w45bqgaydambqgaydambqgaydambq"},
		{"", "did-"},
	} {
		if got := kcpclient.ServiceAccountName(tc.did); got != tc.want {
			t.Errorf("ServiceAccountName(%q) = %q, want %q -- base32, lowercase, no padding, "+
				"of the UTF-8 DID, with the did- prefix (ADR 0020)", tc.did, got, tc.want)
		}
	}

	for _, r := range kcpclient.ServiceAccountName("did:plc:z72i7hdynmk6r22z27h6tvur") {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') && r != '-' {
			t.Fatalf("the derived name carries %q, which is outside base32's [a-z2-7] and "+
				"may not be a valid DNS-1123 label -- RBAC subjects are keyed by NAME, so a "+
				"rejected name is a grant that never lands", r)
		}
	}

}
