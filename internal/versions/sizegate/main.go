package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

const closureLine = "typescript-closure"

var closureFiles = []string{"closure.tar.zst", "manifest.json.zst"}

func main() {
	platform := flag.String("platform", "", "goos/goarch of the release binary")
	binary := flag.String("binary", "", "path of the release binary built with -tags release")
	blobs := flag.String("blobs", "", "the staged blob directory of that platform")
	closure := flag.String("closure", "", "the staged TypeScript closure directory of that platform (internal/tsbundle/closure/<goos>_<goarch>)")
	override := flag.Int64("budget-bytes", 0, "replace the platform's budget with this single total (the gate's own red test)")
	flag.Parse()
	if *platform == "" || *binary == "" || *blobs == "" {
		flag.Usage()
		os.Exit(2)
	}
	ps, err := versions.Platforms()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	p, ok := ps[*platform]
	if !ok || p.Status != versions.PlatformRelease {
		fmt.Fprintf(os.Stderr, "sizegate: %s is not a release platform of versions.json\n", *platform)
		os.Exit(2)
	}
	if *override > 0 {
		p.Budget = map[string]int64{"core": *override}
	}
	m, err := versions.MeasureRelease(*platform, *binary, *blobs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *closure != "" {
		m.Assets[closureLine] = p.Budget[closureLine]
		staged := false
		for _, name := range closureFiles {
			if st, err := os.Stat(filepath.Join(*closure, name)); err == nil && st.Size() > 0 {
				if !staged {
					m.Assets[closureLine] = 0
				}
				staged = true
				m.Core -= st.Size()
				m.Assets[closureLine] += st.Size()
			}
		}
		if !staged {
			m.NotStaged = append(m.NotStaged, closureLine)
		}
		m.Projected = m.Core
		for _, v := range m.Assets {
			m.Projected += v
		}
	}
	m.BudgetUsed = p.BudgetTotal()
	b, _ := json.Marshal(m)
	fmt.Println(string(b))
	if over := versions.CheckBudget(*platform, p, m.Core, m.Assets); len(over) > 0 {
		for _, o := range over {
			fmt.Fprintln(os.Stderr, "sizegate: over budget:", o)
		}
		os.Exit(1)
	}
}
