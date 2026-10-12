package assets

import (
	_ "embed"
	"path/filepath"
)

//go:embed dnsshim.ts
var Shim string

//go:embed dnsprobe.ts
var Probe string

const DNSDirName = ".kcpdns"

const TableFileEnv = "KCP_DNS_TABLE_FILE"

const TableName = "table.json"

var DNSProbeEnv = []string{"KCP_SERVICE_DOMAIN", "KCP_DNS_TABLE", TableFileEnv, "KCP_TOKENS", "KCP_SERVER"}

func DNSTablePath(runsDir string) string {
	return filepath.Join(runsDir, DNSDirName, TableName)
}

func DNSSet(runsDir string) *Set {
	return &Set{
		Dir: filepath.Join(runsDir, DNSDirName),
		Files: map[string][]byte{
			ShimName:  []byte(Shim),
			ProbeName: []byte(Probe),
		},
	}
}

const (
	ShimName = "shim.ts"

	ProbeName = "probe.ts"
)
