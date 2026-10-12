package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: assetpack digest|tar <dir>")
		os.Exit(2)
	}
	dir := os.Args[2]
	switch os.Args[1] {
	case "digest":
		d, err := assets.TreeDigest(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(d)
	case "tar":
		w := bufio.NewWriterSize(os.Stdout, 1<<20)
		if err := assets.WriteTar(w, dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := w.Flush(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: assetpack digest|tar <dir>")
		os.Exit(2)
	}
}
