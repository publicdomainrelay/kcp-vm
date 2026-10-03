package nodevm

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
)

func AdvertiseEndpoint(server string) (portallocator.Endpoint, error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	host, portText, ok := strings.Cut(trimmed, ":")
	if !ok {
		return portallocator.Endpoint{}, fmt.Errorf("nodevm: the server this node was "+
			"built with, %q, names no port, so an address that answers cannot be told from one that "+
			"does not and the node's own dial line cannot be checked against anything", server)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return portallocator.Endpoint{}, fmt.Errorf("nodevm: the port in %q: %w", server, err)
	}
	return portallocator.Endpoint{Address: host, Port: port}, nil
}

func AssertGuestDialledOurs(nodeName, server, consolePath string) error {
	want, err := AdvertiseEndpoint(server)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(consolePath)
	if err != nil {
		return fmt.Errorf("nodevm: reading node %s's console to check which apiserver it "+
			"dialled: %w -- the tenant cluster reporting this node says a k3s agent registered, and "+
			"a console that cannot be read leaves WHICH apiserver it registered with unverified",
			nodeName, err)
	}
	if err := portallocator.AssertAgentDialledOurPort(string(raw), want); err != nil {
		return fmt.Errorf("nodevm: node %s is reported by the tenant cluster, but its own "+
			"account of what it dialled does not stand up: %w. The node joining is not evidence that "+
			"it joined THIS cluster -- two clusters on one host share neither their lease nor their "+
			"CA, and the one that gets the address wrong registers somewhere else and reports an "+
			"x509 error that reads as a certificate fault", nodeName, err)
	}
	return nil
}
