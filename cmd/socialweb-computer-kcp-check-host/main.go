package main

import (
	"context"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/checkhostmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
)

func main() { os.Exit(checkhostmain.Main(context.Background(), foldmain.OS())) }
