package portscan

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const tcpListen = "0A"

const udpBound = "07"

type listenSocket struct {
	Port  int
	Inode uint64
}

func parseProcNetTCP(r io.Reader) ([]listenSocket, error) {
	return parseProcNet(r, tcpListen)
}

func parseProcNetUDP(r io.Reader) ([]listenSocket, error) {
	return parseProcNet(r, udpBound)
}

func parseProcNet(r io.Reader, state string) ([]listenSocket, error) {
	sc := bufio.NewScanner(r)
	var out []listenSocket
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] == "sl" {
			continue
		}
		if len(fields) < 10 {
			return nil, fmt.Errorf("portscan: %q is not a socket-table row", sc.Text())
		}
		if fields[3] != state {
			continue
		}
		_, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			return nil, fmt.Errorf("portscan: local_address %q has no port", fields[1])
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			return nil, fmt.Errorf("portscan: local port %q: %w", portHex, err)
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("portscan: inode %q: %w", fields[9], err)
		}
		out = append(out, listenSocket{Port: int(port), Inode: inode})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("portscan: reading the socket table: %w", err)
	}
	return out, nil
}

func socketInode(link string) (uint64, bool) {
	rest, ok := strings.CutPrefix(link, "socket:[")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, "]")
	if !ok {
		return 0, false
	}
	inode, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return inode, true
}

func walkInodeOwners(root string, want map[uint64]bool) (map[uint64][]int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("portscan: reading the process table %s: %w", root, err)
	}
	out := map[uint64][]int{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(root, entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			inode, ok := socketInode(target)
			if !ok || !want[inode] {
				continue
			}
			out[inode] = append(out[inode], pid)
		}
	}
	return out, nil
}

func listenersIn(root string) (map[int][]int, error) {
	return ownersIn(root, "tcp", parseProcNetTCP)
}

func datagramListenersIn(root string) (map[int][]int, error) {
	return ownersIn(root, "udp", parseProcNetUDP)
}

func ownersIn(root, table string, parse func(io.Reader) ([]listenSocket, error)) (map[int][]int, error) {
	socks, err := readTable(filepath.Join(root, "net", table), true, parse)
	if err != nil {
		return nil, err
	}
	six, err := readTable(filepath.Join(root, "net", table+"6"), false, parse)
	if err != nil {
		return nil, err
	}
	socks = append(socks, six...)

	want := make(map[uint64]bool, len(socks))
	for _, s := range socks {
		want[s.Inode] = true
	}
	owners, err := walkInodeOwners(root, want)
	if err != nil {
		return nil, err
	}
	byPort := map[int][]int{}
	for _, s := range socks {
		if len(owners[s.Inode]) == 0 {
			continue
		}
		byPort[s.Port] = append(byPort[s.Port], owners[s.Inode]...)
	}
	return byPort, nil
}

func readTable(path string, required bool, parse func(io.Reader) ([]listenSocket, error)) ([]listenSocket, error) {
	f, err := os.Open(path)
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("portscan: opening %s: %w", path, err)
	}
	defer f.Close()
	return parse(f)
}
