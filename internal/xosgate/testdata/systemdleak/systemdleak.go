package systemdleak

import (
	"github.com/coreos/go-systemd/v22/dbus"
)

var Connect = dbus.NewUserConnectionContext
