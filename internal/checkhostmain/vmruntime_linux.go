//go:build linux

package checkhostmain

import (
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

func compiledVMRuntime() string { return nodevm.VMMFirecracker }
