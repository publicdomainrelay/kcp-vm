//go:build darwin

package checkhostmain

import (
	"errors"
	"fmt"
	"os"
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
	return 0, fmt.Errorf("check-host: reading the free space under %s is unsupported on darwin; see plan 0012: %w",
		path, errors.ErrUnsupported)
}
