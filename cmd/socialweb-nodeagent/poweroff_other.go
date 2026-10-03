//go:build !linux

package main

import "os"

func powerOff() {
	os.Exit(1)
}
