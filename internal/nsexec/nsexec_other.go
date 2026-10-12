//go:build !linux

package nsexec

import "errors"

var errUnsupported = errors.New("nsexec: a private hostname is Linux-only")

func Enter(c Config, self []string) error { return errUnsupported }

func Inside(c Config) error { return errUnsupported }
