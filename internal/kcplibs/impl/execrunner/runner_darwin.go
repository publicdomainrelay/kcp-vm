//go:build darwin

package execrunner

import (
	"fmt"
	"os/exec"
	"runtime"
)

type osProcessHost struct{}

func (osProcessHost) prepare(*exec.Cmd) error {
	return fmt.Errorf("%w: %s, see plan 0012 M1-DENOPOD-RUNNER-DARWIN", ErrUnsupported, runtime.GOOS)
}

func (osProcessHost) killGroup(int) {}

func (osProcessHost) termGroup(int) {}

func (osProcessHost) alive(int) bool { return false }

func (osProcessHost) startTicks(int) uint64 { return 0 }

func (osProcessHost) environ(int) map[string]string { return nil }
