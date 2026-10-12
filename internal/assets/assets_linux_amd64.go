//go:build release && linux && amd64

package assets

import "embed"

//go:embed blobs/linux_amd64
var blobs embed.FS

var (
	platform = "linux/amd64"
	blobDir  = "blobs/linux_amd64"
)
