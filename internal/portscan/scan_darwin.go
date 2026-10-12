//go:build darwin

package portscan

import (
	"errors"
	"fmt"
)

func listeners() (map[int][]int, error) {
	return nil, fmt.Errorf("portscan: listing TCP listeners is unsupported on darwin; see plan 0012: %w", errors.ErrUnsupported)
}

func datagramListeners() (map[int][]int, error) {
	return nil, fmt.Errorf("portscan: listing UDP listeners is unsupported on darwin; see plan 0012: %w", errors.ErrUnsupported)
}

func cmdline(pid int) ([]string, error) {
	return nil, fmt.Errorf("portscan: reading the command line of pid %d is unsupported on darwin; see plan 0012: %w", pid, errors.ErrUnsupported)
}
