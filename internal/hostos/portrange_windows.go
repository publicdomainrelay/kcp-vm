//go:build windows

package hostos

import (
	"os/exec"
	"regexp"
	"strconv"
)

const (
	defaultDynamicFirst = 49152

	defaultDynamicLast = 65535
)

var dynamicStart = regexp.MustCompile(`(?i)start\s*port\s*:\s*(\d+)`)

var dynamicCount = regexp.MustCompile(`(?i)number\s+of\s+ports\s*:\s*(\d+)`)

func LocalPortRange() (int, int, error) {
	out, err := exec.Command("netsh", "int", "ipv4", "show", "dynamicport", "tcp").Output()
	if err != nil {
		return defaultDynamicFirst, defaultDynamicLast, nil
	}
	start := dynamicStart.FindStringSubmatch(string(out))
	count := dynamicCount.FindStringSubmatch(string(out))
	if start == nil || count == nil {
		return defaultDynamicFirst, defaultDynamicLast, nil
	}
	first, err1 := strconv.Atoi(start[1])
	n, err2 := strconv.Atoi(count[1])
	if err1 != nil || err2 != nil || n < 1 {
		return defaultDynamicFirst, defaultDynamicLast, nil
	}
	return first, first + n - 1, nil
}
