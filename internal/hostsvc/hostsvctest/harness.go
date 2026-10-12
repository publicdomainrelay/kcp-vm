package hostsvctest

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

type Harness struct {
	Open               func(t *testing.T) (hostsvc.Manager, error)
	RestartManager     func(t *testing.T)
	SkipManagerRestart string
	Prefix             string
	SlicePrefix        string
	FDStoreHelper      func(t *testing.T) (argv []string, env []string)
}

type Case struct {
	Name    string
	Methods []string
	Run     func(t *testing.T, h Harness)
}

func RandomKey() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
