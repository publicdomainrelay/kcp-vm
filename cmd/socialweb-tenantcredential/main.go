package main

import (
	"context"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantcredentialmain"
)

func main() { os.Exit(tenantcredentialmain.Main(context.Background(), foldmain.OS())) }
