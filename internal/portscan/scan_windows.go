//go:build windows

package portscan

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

func listeners() (map[int][]int, error) {
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return nil, fmt.Errorf("portscan: running netstat -ano to list this host's TCP listeners: "+
			"%w -- every bound process this package attributes is found through that listing, so a "+
			"listing that cannot be read is a scan that attributes nothing", err)
	}
	owners := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		at := strings.LastIndex(fields[1], ":")
		if at < 0 {
			continue
		}
		port, err := strconv.Atoi(fields[1][at+1:])
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}
		owners[port] = append(owners[port], pid)
	}
	return owners, nil
}

func datagramListeners() (map[int][]int, error) {
	out, err := exec.Command("netstat", "-ano", "-p", "udp").Output()
	if err != nil {
		return nil, fmt.Errorf("portscan: running netstat -ano -p udp to list this host's bound UDP "+
			"sockets: %w -- a link component holds its leased port as a datagram socket, so a "+
			"listing that cannot be read is a scan that attributes no link", err)
	}
	owners := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.EqualFold(fields[0], "UDP") {
			continue
		}
		at := strings.LastIndex(fields[1], ":")
		if at < 0 {
			continue
		}
		port, err := strconv.Atoi(fields[1][at+1:])
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			continue
		}
		owners[port] = append(owners[port], pid)
	}
	return owners, nil
}

func cmdline(pid int) ([]string, error) {
	return hostos.Cmdline(pid)
}
