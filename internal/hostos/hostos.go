package hostos

import (
	"runtime"
)

const (
	NetworkPasta = "pasta"

	NetworkUser = "user"
)

const (
	AccelKVM = "kvm"

	AccelWHPX = "whpx"

	AccelTCG = "tcg"
)

const (
	UserGateway = "10.0.2.2"

	UserGuestIP = "10.0.2.15"

	UserPrefix = 24
)

func Name() string { return runtime.GOOS }

func QEMU() bool { return true }
