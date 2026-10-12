//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "socialweb-vmexec: Linux only (plans 0011 and 0012 own the other hosts)")
	os.Exit(2)
}
