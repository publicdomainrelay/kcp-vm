//go:build linux

package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"
	"syscall"
)

func readDevice(info os.FileInfo) (deviceInfo, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return deviceInfo{}, false
	}
	return deviceInfo{UID: st.Uid, GID: st.Gid, Rdev: st.Rdev}, true
}

func deviceDescription(info os.FileInfo, st deviceInfo) string {
	kind := "a file"
	switch {
	case info.Mode()&os.ModeCharDevice != 0:
		kind = "a character device"
	case info.Mode()&os.ModeDevice != 0:
		kind = "a block device"
	}
	return fmt.Sprintf("%s in mode %04o owned by uid %d gid %s, device numbers %d:%d",
		kind, info.Mode().Perm(), st.UID, groupName(st.GID), major(st.Rdev), minor(st.Rdev))
}

func groupName(gid uint32) string {
	group, err := user.LookupGroupId(fmt.Sprintf("%d", gid))
	if err != nil {
		return fmt.Sprintf("%d", gid)
	}
	return fmt.Sprintf("%d (%s)", gid, group.Name)
}

func major(dev uint64) uint64 { return (dev>>8)&0xfff | ((dev >> 32) &^ 0xfff) }

func minor(dev uint64) uint64 { return dev&0xff | ((dev >> 12) &^ 0xff) }

func selinuxContext(path string) string {
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(path, "security.selinux", buf)
	if err != nil || n == 0 {
		return "no security.selinux attribute"
	}
	return strings.TrimRight(string(buf[:n]), "\x00")
}

func freeSpaceBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
