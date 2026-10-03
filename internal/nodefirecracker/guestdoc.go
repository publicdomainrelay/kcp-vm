package nodefirecracker

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func LoadGuestDocument(path string) (GuestDocument, error) {
	var doc GuestDocument
	raw, err := os.ReadFile(path)
	if err != nil {
		return doc, fmt.Errorf("nodefirecracker: reading %s, which the image carries so the guest "+
			"knows which modules to load and where its k3s lives: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("nodefirecracker: %s: %w", path, err)
	}
	return doc, nil
}

func LoadNodeDocument(path string) (NodeDocument, error) {
	var doc NodeDocument
	raw, err := os.ReadFile(path)
	if err != nil {
		return doc, fmt.Errorf("nodefirecracker: reading %s, which is this node's identity and its "+
			"only source of the join token: %w", path, err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("nodefirecracker: %s is not readable as a node identity, and a guest "+
			"that guesses a node name or a token joins as the wrong node or as none: %w", path, err)
	}
	if doc.NodeName == "" || doc.Token == "" || doc.NodePassword == "" || doc.Server == "" {
		return doc, fmt.Errorf("nodefirecracker: %s carries node name %q, server %q, a token of %d "+
			"bytes and a node password of %d bytes; a guest handed an incomplete identity cannot "+
			"join and cannot say which part was missing", path, doc.NodeName, doc.Server,
			len(doc.Token), len(doc.NodePassword))
	}
	if doc.Interface == "" {
		doc.Interface = DefaultInterface
	}
	if doc.Prefix == 0 {
		return doc, fmt.Errorf("nodefirecracker: %s carries address %q with no prefix, and the "+
			"prefix is what the guest's own agent puts on its interface as `ip addr add "+
			"<address>/<prefix>`. A zero there is a /0: the guest would be told that every address "+
			"that exists is on its own link, so it would ARP for the apiserver, for its registry "+
			"and for every peer instead of routing them through the gateway pasta gave it. The "+
			"prefix was a constant in this package once, and it was a measurement of one host's "+
			"LAN; it now comes from the range the node's cluster holds", path, doc.Address)
	}
	return doc, nil
}
