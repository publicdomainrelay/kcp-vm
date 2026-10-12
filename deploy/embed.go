package deploy

import "embed"

//go:embed *.yaml denokcp/*.yaml eso/*.yaml examples/atproto/market/*.yaml
var FS embed.FS
