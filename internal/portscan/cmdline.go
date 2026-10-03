package portscan

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func cmdlineIn(root string, pid int) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, fmt.Errorf("portscan: reading the command line of %d: %w", pid, err)
	}
	return parseProcCmdline(b), nil
}

func parseProcCmdline(b []byte) []string {
	fields := strings.Split(string(b), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		return fields[:n-1]
	}
	return fields
}

func parsePSCommand(out []byte) []string {
	line := strings.TrimRight(string(out), "\n")
	if line == "" {
		return nil
	}
	return []string{line}
}
