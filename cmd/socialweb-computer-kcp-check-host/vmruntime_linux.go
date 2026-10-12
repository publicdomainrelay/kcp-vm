//go:build linux

package main

import (
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
)

func compiledVMRuntime() string { return nodevm.VMMFirecracker }
