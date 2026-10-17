package execrunner

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
)

var ErrUnsupported = errors.New("execrunner: no process runner for this OS")

var unitAttribution = []string{"SWC_UNIT", "SWC_DEVSUP", "INVOCATION_ID"}

type processHost interface {
	prepare(cmd *exec.Cmd) error
	killGroup(pid int)
	termGroup(pid int)
	alive(pid int) bool
	startTicks(pid int) uint64
	environ(pid int) map[string]string
}

var host processHost = osProcessHost{}

func detached(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(unitAttribution, name)
	})
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
