package cloudprovider

import (
	"errors"
	"fmt"
	"strings"
)

const ProviderIDPrefix = "socialweb://"

var ErrBadProviderID = errors.New(
	"cloudprovider: a provider ID is socialweb://<cluster>/<nodeName> -- one cluster name, one " +
		"node name, separated by a slash -- and this is not one. The parser is strict because the " +
		"string it rejects is what the apiserver records as a Node's identity: a malformed one " +
		"that parsed would stamp every Node with a name no reader can take apart again")

func ProviderID(cluster, nodeName string) string {
	return ProviderIDPrefix + cluster + "/" + nodeName
}

func ParseProviderID(s string) (cluster, nodeName string, err error) {
	if !strings.HasPrefix(s, ProviderIDPrefix) {
		return "", "", fmt.Errorf("%w: %q does not begin with %q", ErrBadProviderID, s, ProviderIDPrefix)
	}
	rest := strings.TrimPrefix(s, ProviderIDPrefix)
	var ok bool
	cluster, nodeName, ok = strings.Cut(rest, "/")
	if !ok {
		return "", "", fmt.Errorf(
			"%w: %q names no node, because the cluster and the node are separated by exactly one "+
				"slash", ErrBadProviderID, s)
	}
	if cluster == "" {
		return "", "", fmt.Errorf("%w: %q has an empty cluster name", ErrBadProviderID, s)
	}
	if nodeName == "" {
		return "", "", fmt.Errorf("%w: %q has an empty node name", ErrBadProviderID, s)
	}
	if !isASCII(cluster) || !isASCII(nodeName) {
		return "", "", fmt.Errorf(
			"%w: %q carries a byte above 0x7F; a provider ID is a wire identity and only ASCII "+
				"round-trips through every reader", ErrBadProviderID, s)
	}
	if strings.Contains(nodeName, "/") {
		return "", "", fmt.Errorf(
			"%w: %q names the node %q, which itself holds a slash. A node name is a single "+
				"DNS-1123 label, and accepting a second slash would let two different inputs "+
				"produce one provider ID", ErrBadProviderID, s, nodeName)
	}
	return cluster, nodeName, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7F {
			return false
		}
	}
	return true
}
