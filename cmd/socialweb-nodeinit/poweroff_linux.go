//go:build linux

package main

import "syscall"

func powerOff() {
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
