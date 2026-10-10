package vaultlayout

import "testing"

func TestTheNodeJWTNamesAreTheOnesTheContractNames(t *testing.T) {
	node := "c0ffee-node3"
	for _, tc := range []struct{ label, got, want string }{
		{"kv data path", NodeKVDataPath(node), "socialweb-nodes/data/c0ffee-node3"},
		{"kv metadata path", NodeKVMetadataPath(node), "socialweb-nodes/metadata/c0ffee-node3"},
		{"policy", NodeJWTPolicyName(node), "node-c0ffee-node3"},
		{"role", NodeJWTRoleName(node), "c0ffee-node3"},
		{"policy hcl", NodeJWTPolicyHCL(node),
			"path \"socialweb-nodes/data/c0ffee-node3\" {\n  capabilities = [\"read\"]\n}\n"},
		{"mount", NodeJWTMount("https://bidder.example.com"), "jwt-5860b7149f69"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q -- the guest builds the same name from claim.json and the "+
				"door's grant, so a name that drifts here is a login that lands on nothing", tc.label,
				tc.got, tc.want)
		}
	}
	if !IsNodeJWTMount(NodeJWTMount("https://bidder.example.com") + "/") {
		t.Errorf("a mount this package names is not recognised as one")
	}
	for _, other := range []string{"approle/", "cert/", "jwt/", "jwt-zzzzzzzzzzzz/", "jwt-5f45e2b8f6a/"} {
		if IsNodeJWTMount(other) {
			t.Errorf("%q is read as a node JWT mount, and the release would delete roles under it", other)
		}
	}
}
