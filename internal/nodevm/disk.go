package nodevm

import (
	"errors"
	"fmt"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/instance"
)

func MintOrReadNodePassword(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		return string(raw), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("nodevm: reading this node's own node password at %s: %w -- "+
			"this credential is per node and unlike the join token; k3s refuses a password it did "+
			"not issue for that node name as a duplicate hostname", path, err)
	}
	password, err := instance.New(nil)
	if err != nil {
		return "", fmt.Errorf("nodevm: minting a per-node password for this node: %w", err)
	}
	if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
		return "", fmt.Errorf("nodevm: writing this node's own password to %s: %w -- it "+
			"is NOT the cluster join token and must never become one", path, err)
	}
	return string(password), nil
}
