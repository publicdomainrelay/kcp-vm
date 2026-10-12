package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

func main() {
	platform := flag.String("platform", "", "goos/goarch of the release binary")
	binary := flag.String("binary", "", "path of the release binary built with -tags release")
	blobs := flag.String("blobs", "", "the staged blob directory of that platform")
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
