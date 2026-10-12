//go:build !linux

package nodevm

import (
	"io"
	"os"
)

func cloneInto(out, in *os.File) error {
	_, err := io.Copy(out, in)
	return err
}
