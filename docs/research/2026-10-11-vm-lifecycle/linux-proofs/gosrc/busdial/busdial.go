package busdial

import (
	"context"
	"fmt"
	"os"
	"strconv"

	sd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

func PrivatePath() string {
	return os.Getenv("XDG_RUNTIME_DIR") + "/systemd/private"
}

func RawPrivate(ctx context.Context, path string) (*dbus.Conn, error) {
	c, err := dbus.Dial("unix:path="+path, dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if err := c.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func RawSession(ctx context.Context, addr string) (*dbus.Conn, error) {
	c, err := dbus.Dial(addr, dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if err := c.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.Hello(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func Raw(ctx context.Context, mode, path string) (*dbus.Conn, error) {
	switch mode {
	case "private":
		if path == "" {
			path = PrivatePath()
		}
		return RawPrivate(ctx, path)
	case "session":
		addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
		if path != "" {
			addr = "unix:path=" + path
		}
		return RawSession(ctx, addr)
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

func Systemd(ctx context.Context, mode, path string) (*sd.Conn, error) {
	if mode == "session" && path == "" {
		return sd.NewUserConnectionContext(ctx)
	}
	return sd.NewConnection(func() (*dbus.Conn, error) { return Raw(ctx, mode, path) })
}
