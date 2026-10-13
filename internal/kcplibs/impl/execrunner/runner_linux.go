//go:build linux

package execrunner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type osProcessHost struct{}

func (osProcessHost) prepare(cmd *exec.Cmd, unit string) error {
	cmd.Env = unitEnv(cmd.Env, unit)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func (osProcessHost) killGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

func (osProcessHost) termGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
}

func (osProcessHost) alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (osProcessHost) startTicks(pid int) uint64 {
	_, ticks, err := readProcessStat(pid)
	if err != nil {
		return 0
	}
	return ticks
}

func readProcessStat(pid int) (string, uint64, error) {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0, err
	}
	line := string(body)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 > len(line) {
		return "", 0, errors.New("execrunner: unreadable process stat")
	}
	comm := line[strings.Index(line, "(")+1 : end]
	fields := strings.Fields(line[end+2:])
	if len(fields) < 20 {
		return "", 0, errors.New("execrunner: short process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return "", 0, err
	}
	return comm, ticks, nil
}
