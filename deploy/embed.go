package deploy

import "embed"

//go:embed *.yaml denokcp/*.yaml denokcp/vendor.json eso/*.yaml examples/atproto/market/*.yaml
var FS embed.FS
