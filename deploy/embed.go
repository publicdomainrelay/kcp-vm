package deploy

import "embed"

//go:embed *.yaml denokcp/*.yaml denokcp/vendor.json eso/*.yaml market-network/*.yaml
var FS embed.FS
