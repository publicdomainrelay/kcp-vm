//go:build linux

package assets

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func ProcessesOfUID(uid int, match func(argv []string) bool) []int {
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat(filepath.Join("/proc", d.Name()), &st) != nil || int(st.Uid) != uid {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", d.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		if match(strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")) {
			out = append(out, pid)
		}
	}
	return out
}
