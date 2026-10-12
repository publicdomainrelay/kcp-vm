package main

import (
	"context"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultmain"
)

func main() { os.Exit(vaultmain.Main(context.Background(), foldmain.OS())) }
