//go:build release && linux && arm64

package assets

import "embed"

//go:embed blobs/linux_arm64
var blobs embed.FS

var (
	platform = "linux/arm64"
	blobDir  = "blobs/linux_arm64"
)
