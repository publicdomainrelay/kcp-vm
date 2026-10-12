package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func status(keys ...string) string {
	raw, _ := os.ReadFile("/proc/self/status")
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		for _, k := range keys {
			if strings.HasPrefix(l, k+":") {
				out = append(out, strings.Join(strings.Fields(l), " "))
			}
		}
	}
	return strings.Join(out, " | ")
}

func die(step string, err error) {
	fmt.Fprintf(os.Stderr, "tapexec: %s: %v\n", step, err)
	os.Exit(1)
}

func setIff(name string, flags uint16) (int, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return -1, err
	}
	ifr.SetUint16(flags)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("TUNSETIFF: %w", err)
	}
	return fd, nil
}

func linkUp(name string) error {
	s, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(s)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(s, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("SIOCGIFFLAGS: %w", err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(s, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("SIOCSIFFLAGS: %w", err)
	}
	return nil
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "attach" {
		name := os.Args[2]
		fd, err := setIff(name, unix.IFF_TAP|unix.IFF_NO_PI|unix.IFF_VNET_HDR)
		if err != nil {
			die("attach "+name, err)
		}
		fmt.Printf("attach: pid=%d opened existing %s with IFF_TAP|IFF_NO_PI|IFF_VNET_HDR fd=%d (as firecracker does for host_dev_name)\n", os.Getpid(), name, fd)
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(os.Args) < 3 {
		die("usage", fmt.Errorf("tapexec <tap> <argv...> | tapexec attach <tap>"))
	}
	name := os.Args[1]
	fmt.Printf("tapexec: pid=%d ppid=%d %s\n", os.Getpid(), os.Getppid(), status("Uid", "CapEff"))
	uidmap, _ := os.ReadFile("/proc/self/uid_map")
	fmt.Printf("tapexec: uid_map=%q\n", strings.TrimSpace(string(uidmap)))
	fd, err := setIff(name, unix.IFF_TAP|unix.IFF_NO_PI)
	if err != nil {
		die("create "+name, err)
	}
	if err := unix.IoctlSetInt(fd, unix.TUNSETPERSIST, 1); err != nil {
		die("TUNSETPERSIST", err)
	}
	unix.Close(fd)
	if err := linkUp(name); err != nil {
		die("link up", err)
	}
	fmt.Printf("tapexec: created persistent %s, up; execve %v\n", name, os.Args[2:])
	if err := syscall.Exec(os.Args[2], os.Args[2:], os.Environ()); err != nil {
		die("execve", err)
	}
}
