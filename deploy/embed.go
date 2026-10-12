package deploy

import "embed"

//go:embed *.yaml apps/*.yaml apps/*/*.yaml denokcp/*.yaml denokcp/vendor.json denopods/*.yaml eso/*.yaml market-network/*.yaml
var FS embed.FS
