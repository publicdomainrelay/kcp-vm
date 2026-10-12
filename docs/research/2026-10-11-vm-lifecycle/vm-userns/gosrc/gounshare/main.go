package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func threads() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "Threads:") {
			return strings.TrimSpace(l[len("Threads:"):])
		}
	}
	return "?"
}

func main() {
	err := syscall.Unshare(syscall.CLONE_NEWUSER)
	fmt.Printf("threads=%s unshare(CLONE_NEWUSER)=%v\n", threads(), err)
	if err != nil {
		os.Exit(1)
	}
}
