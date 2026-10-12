//go:build windows

package execrunner

import (
	"fmt"
	"os/exec"
	"runtime"
)

type osProcessHost struct{}

func (osProcessHost) prepare(*exec.Cmd, string) error {
	return fmt.Errorf("%w: %s, see plan 0011 M1-DENOPOD-RUNNER-WINDOWS", ErrUnsupported, runtime.GOOS)
}

func (osProcessHost) killGroup(int) {}

func (osProcessHost) alive(int) bool { return false }

func (osProcessHost) startTicks(int) uint64 { return 0 }
