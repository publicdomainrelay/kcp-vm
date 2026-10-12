//go:build windows

package checkhostmain

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func readDevice(info os.FileInfo) (deviceInfo, bool) {
	return deviceInfo{}, false
}

func deviceDescription(info os.FileInfo, st deviceInfo) string {
	kind := "a file"
	switch {
	case info.Mode()&os.ModeCharDevice != 0:
		kind = "a character device"
	case info.Mode()&os.ModeDevice != 0:
		kind = "a block device"
	}
	return fmt.Sprintf("%s in mode %04o, %d bytes", kind, info.Mode().Perm(), info.Size())
}

func selinuxContext(path string) string {
	return "SELinux does not apply on this platform"
}

func freeSpaceBytes(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, avail uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err != nil {
		return 0, err
	}
	return int64(free), nil
}
