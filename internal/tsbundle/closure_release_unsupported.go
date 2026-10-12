//go:build release && !(linux && (amd64 || arm64))

package tsbundle

import "embed"

var closureFS embed.FS

const closureDir = ""
