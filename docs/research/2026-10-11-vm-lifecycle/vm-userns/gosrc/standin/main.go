package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	kvmGetAPIVersion = 0xAE00
	kvmCreateVM      = 0xAE01
	tunSetIff        = 0x400454ca
	tunSetPersist    = 0x400454cb
	iffTap           = 0x0002
	iffNoPi          = 0x1000
)

func line(k, v string) { fmt.Printf("%s=%s\n", k, strings.TrimSpace(v)) }

func file(k, p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		line(k, "ERR "+err.Error())
		return
	}
	line(k, strings.Join(strings.Fields(string(b)), " "))
}

func status() {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		for _, p := range []string{"Uid:", "Gid:", "Groups:", "CapEff:"} {
			if strings.HasPrefix(l, p) {
				line(strings.ToLower(strings.TrimSuffix(p, ":")), strings.Join(strings.Fields(l[len(p):]), " "))
			}
		}
	}
}

func kvm() {
	fd, err := syscall.Open("/dev/kvm", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		line("kvm_open", "ERR "+err.Error())
		return
	}
	v, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), kvmGetAPIVersion, 0)
	line("kvm_open", fmt.Sprintf("OK api=%d err=%v", v, e))
	vm, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), kvmCreateVM, 0)
	if e != 0 {
		line("kvm_create_vm", "ERR "+e.Error())
		return
	}
	line("kvm_create_vm", fmt.Sprintf("OK fd=%d", vm))
}

func tap(name string) {
	fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		line("tap", "ERR open "+err.Error())
		return
	}
	var req [40]byte
	copy(req[:16], name)
	*(*uint16)(unsafe.Pointer(&req[16])) = iffTap | iffNoPi
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tunSetIff, uintptr(unsafe.Pointer(&req[0]))); e != 0 {
		line("tap", "ERR TUNSETIFF "+e.Error())
		return
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tunSetPersist, 1); e != 0 {
		line("tap", "ERR TUNSETPERSIST "+e.Error())
		return
	}
	line("tap", "OK "+name+" persistent")
}

func probe(paths []string) {
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			line("probe "+p, "DENIED "+err.Error())
			continue
		}
		f.Close()
		line("probe "+p, "READABLE")
	}
}

func main() {
	signal.Ignore(syscall.SIGTERM)
	if out, err := os.OpenFile(os.Args[1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		os.Stdout = out
	}
	line("pid", fmt.Sprint(os.Getpid()))
	line("ppid", fmt.Sprint(os.Getppid()))
	status()
	file("uid_map", "/proc/self/uid_map")
	file("gid_map", "/proc/self/gid_map")
	file("setgroups", "/proc/self/setgroups")
	file("selinux", "/proc/self/attr/current")
	tap("fctap0")
	kvm()
	probe(os.Args[2:])
	line("ready", time.Now().UTC().Format(time.RFC3339Nano))
	for {
		time.Sleep(time.Hour)
	}
}
