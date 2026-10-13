//go:build integration

package integration

import "testing"

func TestHarnessEmptyNetworkUpAndDown(t *testing.T) {
	for _, name := range []string{"a", "b"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n := StartNetwork(t, NetworkOptions{Bidders: 1})
			vms, err := n.ComputeVMs()
			if err != nil {
				t.Fatal(err)
			}
			if len(vms) != 0 {
				t.Fatalf("a fresh market network lists ComputeVMs: %v", vms)
			}
		})
	}
}
