//go:build !linux

package guestdisk

import (
	"io"
	"os"
)

func cloneInto(out, in *os.File) error {
	_, err := io.Copy(out, in)
	return err
}
