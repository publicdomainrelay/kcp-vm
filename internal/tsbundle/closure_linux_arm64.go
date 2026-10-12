//go:build release && linux && arm64

package tsbundle

import "embed"

//go:embed closure/linux_arm64
var closureFS embed.FS

const closureDir = "closure/linux_arm64"
