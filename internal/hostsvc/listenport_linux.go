//go:build linux

package hostsvc

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const tcpListenState = "0A"

func ListenPort(pid int) (int, error) {
	return listenPortIn("/proc", pid)
}

func listenPortIn(root string, pid int) (int, error) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	inodes, err := socketInodes(filepath.Join(dir, "fd"))
	if err != nil {
		return 0, err
	}
	ports := map[int]bool{}
	for _, table := range []string{"tcp", "tcp6"} {
		found, err := listenRows(filepath.Join(dir, "net", table), inodes)
		if err != nil {
			return 0, err
		}
		for _, p := range found {
			ports[p] = true
		}
	}
	switch len(ports) {
	case 0:
		return 0, fmt.Errorf("%w: pid %d", ErrNoListener, pid)
	case 1:
		for p := range ports {
			return p, nil
		}
	}
	var all []int
	for p := range ports {
		all = append(all, p)
	}
	sort.Ints(all)
	return 0, fmt.Errorf("%w: pid %d listens on %v; the single-socket rule refuses to guess", ErrManyListeners, pid, all)
}

func socketInodes(fdDir string) (map[string]bool, error) {
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, fmt.Errorf("hostsvc: reading %s: %w", fdDir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			out[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	return out, nil
}

func listenRows(path string, inodes map[string]bool) ([]int, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hostsvc: reading %s: %w", path, err)
	}
	defer f.Close()
	var ports []int
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != tcpListenState || !inodes[fields[9]] {
			continue
		}
		i := strings.LastIndexByte(fields[1], ':')
		if i < 0 {
			continue
		}
		port, err := strconv.ParseUint(fields[1][i+1:], 16, 16)
		if err != nil {
			return nil, fmt.Errorf("hostsvc: %s local address %q: %w", path, fields[1], err)
		}
		ports = append(ports, int(port))
	}
	return ports, sc.Err()
}
