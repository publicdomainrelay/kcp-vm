//go:build !linux

package install

import "io"

func NewSystem(out io.Writer) (System, error) { return nil, ErrUnsupported }
