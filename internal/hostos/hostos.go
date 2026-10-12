package hostos

import (
	"runtime"
)

const (
	UserGateway = "10.0.2.2"

	UserGuestIP = "10.0.2.15"

	UserPrefix = 24
)

func Name() string { return runtime.GOOS }
