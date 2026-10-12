//go:build !linux

package hostos

import (
	"errors"
	"fmt"
	"runtime"
)

func unsupported(what, plan string) error {
	return fmt.Errorf("hostos: %s is unsupported on %s; see plan %s: %w", what, runtime.GOOS, plan, errors.ErrUnsupported)
}
