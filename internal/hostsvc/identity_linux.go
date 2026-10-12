//go:build linux

package hostsvc

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const bootIDPath = "/proc/sys/kernel/random/boot_id"

const statFieldStartTime = 22

var errProcessGone = errors.New("hostsvc: the process is gone")

type pinned interface {
	FD() int
	StartTicks() uint64
	Close()
}

type procs interface {
	Pin(pid int) (pinned, error)
	BootID() (string, error)
}

type linuxProcs struct{}

type pidfd struct {
	fd    int
	ticks uint64
}

func (p *pidfd) FD() int { return p.fd }

func (p *pidfd) StartTicks() uint64 { return p.ticks }

func (p *pidfd) Close() { unix.Close(p.fd) }

func (linuxProcs) Pin(pid int) (pinned, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("%w: pid %d", errProcessGone, pid)
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, fmt.Errorf("%w: pidfd_open(%d): %v", errProcessGone, pid, err)
	}
	if err != nil {
		return nil, fmt.Errorf("hostsvc: pidfd_open(%d): %w", pid, err)
	}
	ticks, err := startTicks(pid)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := unix.PidfdSendSignal(fd, 0, nil, 0); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.ESRCH) {
			return nil, fmt.Errorf("%w: pidfd_send_signal(%d, 0): %v", errProcessGone, pid, err)
		}
		return nil, fmt.Errorf("hostsvc: pidfd_send_signal(%d, 0): %w", pid, err)
	}
	return &pidfd{fd: fd, ticks: ticks}, nil
}

func (linuxProcs) BootID() (string, error) {
	b, err := os.ReadFile(bootIDPath)
	if err != nil {
		return "", fmt.Errorf("hostsvc: reading the boot id: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func startTicks(pid int) (uint64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("%w: /proc/%d/stat: %v", errProcessGone, pid, err)
	}
	if err != nil {
		return 0, fmt.Errorf("hostsvc: reading /proc/%d/stat: %w", pid, err)
	}
	state, ticks, err := parseStat(b)
	if err != nil {
		return 0, err
	}
	if state == 'Z' || state == 'X' {
		return 0, fmt.Errorf("%w: pid %d is in state %c", errProcessGone, pid, state)
	}
	return ticks, nil
}

func parseStat(b []byte) (byte, uint64, error) {
	comm := bytes.LastIndexByte(b, ')')
	if comm < 0 {
		return 0, 0, fmt.Errorf("hostsvc: a /proc/<pid>/stat with no ) closing the comm field")
	}
	fields := strings.Fields(string(b[comm+1:]))
	start := statFieldStartTime - 3
	if len(fields) <= start || len(fields[0]) != 1 {
		return 0, 0, fmt.Errorf("hostsvc: /proc/<pid>/stat carries %d fields after the comm field, too few for the start time",
			len(fields))
	}
	n, err := strconv.ParseUint(fields[start], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("hostsvc: /proc/<pid>/stat start time %q: %w", fields[start], err)
	}
	return fields[0][0], n, nil
}
