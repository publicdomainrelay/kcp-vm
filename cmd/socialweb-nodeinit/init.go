//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const initModulesDir = "/modules"

func initLogf(format string, a ...any) {
	fmt.Fprintf(os.Stdout, "KCP_INIT "+format+"\n", a...)
}

func InitGuest() error {
	for _, m := range []struct{ src, dst, fstype string }{
		{"proc", "/proc", "proc"}, {"sysfs", "/sys", "sysfs"}, {"devtmpfs", "/dev", "devtmpfs"},
	} {
		if err := syscall.Mount(m.src, m.dst, m.fstype, 0, ""); err != nil {
			initLogf("mount %s -> %s failed: %v", m.fstype, m.dst, err)
		}
	}
	entries, err := os.ReadDir(initModulesDir)
	if err != nil {
		return fmt.Errorf("nodeinit: reading %s, which is where the initramfs carries the virtio "+
			"transport the guest's block device arrives on: %w -- without virtio_mmio the guest has "+
			"no /dev/vda, and the failure that follows is a kernel panic about a missing root device "+
			"that names none of this", initModulesDir, err)
	}
	for _, e := range entries {
		if err := loadModule(e.Name(), initModulesDir+"/"+e.Name()); err != nil {
			initLogf("insmod %s failed: %v", e.Name(), err)
		}
	}
	var waited time.Duration
	for i := 0; i < 200; i++ {
		if _, err := os.Stat("/dev/vda"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
		waited += 50 * time.Millisecond
	}
	if _, err := os.Stat("/dev/vda"); err != nil {
		initLogf("vda never appeared (waited %s)", waited)
		return err
	}
	initLogf("vda present after %s", waited)
	if err := syscall.Mount("/dev/vda", "/newroot", "ext4", 0, ""); err != nil {
		return fmt.Errorf("nodeinit: mounting the root filesystem at /dev/vda as ext4: %w -- the "+
			"disk the VMM attached is the root filesystem this initramfs exists to hand over to, "+
			"and a mount that fails here leaves the guest with no init at all", err)
	}
	for _, d := range []string{"proc", "sys", "dev"} {
		if err := os.MkdirAll("/newroot"+d, 0o755); err != nil {
			return fmt.Errorf("nodeinit: creating /newroot%s: %w", d, err)
		}
		if err := syscall.Mount(d, "/newroot"+d, "", syscall.MS_MOVE, ""); err != nil {
			return fmt.Errorf("nodeinit: moving %s into the root filesystem: %w -- the guest's "+
				"systemd re-mounts these itself, but a log line from before it does lands outside "+
				"the journal, and a failure here is what makes a boot that dies early say nothing",
				d, err)
		}
	}
	if err := os.Chdir("/newroot"); err != nil {
		return fmt.Errorf("nodeinit: entering /newroot: %w", err)
	}
	if err := syscall.Mount("/newroot", "/", "", syscall.MS_MOVE, ""); err != nil {
		return fmt.Errorf("nodeinit: moving the root filesystem onto /: %w", err)
	}
	if err := syscall.Chroot("."); err != nil {
		return fmt.Errorf("nodeinit: chrooting into the root filesystem: %w", err)
	}
	initLogf("switch_root -> /sbin/init")
	return syscall.Exec("/sbin/init", []string{"/sbin/init"}, os.Environ())
}

var loadedModules = map[string]bool{}

func loadModule(name, path string) error {
	if loadedModules[name] {
		return nil
	}
	out, err := exec.Command("/bin/busybox", "insmod", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("busybox insmod %s: %v: %s", path, err, strings.TrimSpace(string(out)))
	}
	loadedModules[name] = true
	return nil
}
