package main

import (
	"context"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/providermain"
)

func main() { os.Exit(providermain.Main(context.Background(), foldmain.OS())) }
