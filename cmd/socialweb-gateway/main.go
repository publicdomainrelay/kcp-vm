package main

import (
	"context"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gatewaymain"
)

func main() { os.Exit(gatewaymain.Main(context.Background(), foldmain.OS())) }
