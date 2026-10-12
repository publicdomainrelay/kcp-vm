//go:build linux

package hostos

import (
	"fmt"
	"os"
	"strings"
)

func BootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("hostos: reading /proc/sys/kernel/random/boot_id: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("hostos: /proc/sys/kernel/random/boot_id is empty")
	}
	return id, nil
}
