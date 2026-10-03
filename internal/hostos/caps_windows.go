//go:build windows

package hostos

func Firecracker() bool { return false }

func NetworkMode() string { return NetworkUser }

func Accel() string { return AccelWHPX }
