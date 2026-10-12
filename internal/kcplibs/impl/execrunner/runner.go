package execrunner

import (
	"errors"
	"os/exec"
)

const UnitEnv = "SWC_UNIT"

var ErrUnsupported = errors.New("execrunner: no process runner for this OS")

type processHost interface {
	prepare(cmd *exec.Cmd, unit string) error
	killGroup(pid int)
	termGroup(pid int)
	alive(pid int) bool
	startTicks(pid int) uint64
}

var host processHost = osProcessHost{}

func unitEnv(env []string, unit string) []string {
	if unit == "" {
		return env
	}
	return append(env, UnitEnv+"="+unit)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return host.alive(pid)
}

func startTicks(pid int) uint64 {
	return host.startTicks(pid)
}

func ownedByUs(pid int, ticks uint64) bool {
	if pid <= 0 || ticks == 0 {
		return false
	}
	return host.startTicks(pid) == ticks
}
