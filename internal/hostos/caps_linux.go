//go:build linux

package hostos

import (
	"os"
)

func Firecracker() bool { return true }

func NetworkMode() string { return NetworkPasta }

func Accel() string {
	if _, err := os.Stat("/dev/kvm"); err == nil {
		return AccelKVM
	}
	return AccelTCG
}
