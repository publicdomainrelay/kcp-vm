package main

import (
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func TestTheGuestNodePasswordFilesCarryThePerNodePasswordNotTheToken(t *testing.T) {
	node := nodefirecracker.NodeDocument{
		Token:        "k10clusterwidejoinsecrettoken",
		NodePassword: "np-thisnodesowncredential",
	}
	const dataDir = "/var/lib/rancher/k3s"
	writes := identityWrites(node, dataDir)
	byPath := make(map[string]string, len(writes))
	for _, w := range writes {
		byPath[w.path] = w.value
	}
	if got := byPath[nodefirecracker.GuestTokenPath]; got != node.Token {
		t.Fatalf("the file k3s --token-file names carries %q, want the cluster join token", got)
	}
	for _, path := range []string{"/etc/rancher/node/password", dataDir + "/agent/node-password"} {
		got, ok := byPath[path]
		if !ok {
			t.Fatalf("the guest agent never writes %s, so k3s would mint a password the server "+
				"cannot have issued and the node is refused as a duplicate hostname", path)
		}
		if got == node.Token {
			t.Fatalf("%s carries the CLUSTER JOIN TOKEN: SEC-6 is one value with two roles "+
				"shared by every node, so reading one node's disk hands over the whole cluster", path)
		}
		if got != node.NodePassword {
			t.Fatalf("%s carries %q, want this node's own password", path, got)
		}
	}
}
