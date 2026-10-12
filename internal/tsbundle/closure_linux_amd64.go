//go:build release && linux && amd64

package tsbundle

import "embed"

//go:embed closure/linux_amd64
var closureFS embed.FS

const closureDir = "closure/linux_amd64"
