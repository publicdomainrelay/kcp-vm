package nodeprovider

import "testing"

func TestBadNamesAreRefusedNotSanitised(t *testing.T) {
	for _, tc := range []struct{ cluster, node string }{
		{"", "n1"}, {"web", ""}, {"Web", "N1!"}, {"web", "n 1"},
	} {
		if got, err := NodeName(tc.cluster, tc.node); err == nil {
			t.Fatalf("NodeName(%q,%q) = %q; a name we silently rewrote would no longer "+
				"correlate with the pair it came from", tc.cluster, tc.node, got)
		}
	}
	if got, err := NodeName("Web", "N1"); err != nil || got != "web-n1" {
		t.Fatalf("NodeName(Web,N1) = %q, %v", got, err)
	}
	if got, err := NodeName("liveweb", "node1"); err != nil || got != "liveweb-node1" {
		t.Fatalf("NodeName(liveweb,node1) = %q, %v", got, err)
	}
}
