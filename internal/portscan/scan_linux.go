//go:build linux

package portscan

func listeners() (map[int][]int, error) {
	return listenersIn("/proc")
}

func cmdline(pid int) ([]string, error) {
	return cmdlineIn("/proc", pid)
}
