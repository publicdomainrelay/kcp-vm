//go:build !linux

package main

import (
	"errors"
	"fmt"
	"runtime"
)

func withStageDirHeld(cfg Config, build func() error) error {
	return fmt.Errorf("socialweb-nodeimage: holding the stage directory %s is unsupported on %s; see plans 0011 and 0012: %w",
		cfg.StageDir, runtime.GOOS, errors.ErrUnsupported)
}
