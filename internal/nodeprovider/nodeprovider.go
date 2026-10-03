package nodeprovider

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var dns1123 = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

var ErrBadName = errors.New("nodeprovider: derived node name is not DNS-1123")

func NodeName(clusterLabel, nodeID string) (string, error) {
	if clusterLabel == "" || nodeID == "" {
		return "", fmt.Errorf("%w: cluster and node id are both required", ErrBadName)
	}
	name := strings.ToLower(clusterLabel + "-" + nodeID)
	if !dns1123.MatchString(name) {
		return "", fmt.Errorf("%w: %q", ErrBadName, name)
	}
	return name, nil
}
