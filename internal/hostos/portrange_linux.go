//go:build linux

package hostos

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func LocalPortRange() (int, int, error) {
	out, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, fmt.Errorf("hostos: reading /proc/sys/net/ipv4/ip_local_port_range: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("hostos: /proc/sys/net/ipv4/ip_local_port_range is %q, expected two numbers",
			strings.TrimSpace(string(out)))
	}
	first, err1 := strconv.Atoi(fields[0])
	last, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("hostos: /proc/sys/net/ipv4/ip_local_port_range is %q, expected two numbers",
			strings.TrimSpace(string(out)))
	}
	return first, last, nil
}
