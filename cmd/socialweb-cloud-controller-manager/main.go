package main

import (
	"context"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/ccmmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
)

func main() { os.Exit(ccmmain.Main(context.Background(), foldmain.OS())) }
