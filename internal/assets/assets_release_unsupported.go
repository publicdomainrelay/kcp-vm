//go:build release && !(linux && (amd64 || arm64))

package assets

import (
	"embed"
	"runtime"
)

var blobs embed.FS

var (
	platform = runtime.GOOS + "/" + runtime.GOARCH
	blobDir  = ""
)
