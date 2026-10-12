package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"swproof/busdial"
)

type Record struct {
	PID          int
	StartTicks   uint64
	Unit         string
	InvocationID string
}

func startTicks(pid int) (uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	f := strings.Fields(s[i+2:])
	return strconv.ParseUint(f[19], 10, 64)
}

func call(ctx context.Context, c *dbus.Conn, method string, args ...any) *dbus.Call {
	return c.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").CallWithContext(ctx, "org.freedesktop.systemd1.Manager."+method, 0, args...)
}

func unitInvocation(ctx context.Context, c *dbus.Conn, path dbus.ObjectPath) (string, string, error) {
	o := c.Object("org.freedesktop.systemd1", path)
	var id, inv dbus.Variant
	if err := o.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.systemd1.Unit", "Id").Store(&id); err != nil {
		return "", "", err
	}
	if err := o.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.systemd1.Unit", "InvocationID").Store(&inv); err != nil {
		return "", "", err
	}
	return id.Value().(string), hex.EncodeToString(inv.Value().([]byte)), nil
}

func classifyBusErr(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		switch de.Name {
		case "org.freedesktop.systemd1.NoUnitForPID", "org.freedesktop.systemd1.NoSuchUnit", "org.freedesktop.systemd1.NoSuchProcess":
			return "Lost(" + de.Name + ")"
		}
		return "Unknown(dbus " + de.Name + ")"
	}
	return "Unknown(transport: " + err.Error() + ")"
}

func main() {
	mode := flag.String("mode", "private", "session|private")
	busPath := flag.String("bus", "", "override socket path")
	pid := flag.Int("pid", 0, "pid")
	unit := flag.String("unit", "", "expected unit")
	inv := flag.String("inv", "", "expected invocation id")
	ticks := flag.Uint64("ticks", 0, "expected start ticks (0 = learn)")
	hold := flag.Duration("hold", 0, "hold pidfd open, then recheck after this delay")
	flag.Parse()
	fmt.Printf("ident pid=%d expect unit=%s inv=%s ticks=%d\n", *pid, *unit, *inv, *ticks)

	pfd, err := unix.PidfdOpen(*pid, 0)
	if err != nil {
		fmt.Printf("pidfd_open: %v -> verdict Lost(process gone)\n", err)
		os.Exit(0)
	}
	fmt.Printf("pidfd_open ok fd=%d\n", pfd)
	st, err := startTicks(*pid)
	fmt.Printf("stat starttime(field22)=%v err=%v\n", st, err)
	if err := unix.PidfdSendSignal(pfd, 0, nil, 0); err != nil {
		fmt.Printf("pidfd_send_signal(0) after stat read: %v -> verdict Lost\n", err)
		os.Exit(0)
	}
	if *ticks != 0 && st != *ticks {
		fmt.Printf("start ticks mismatch %d != %d -> verdict Lost(pid reused)\n", st, *ticks)
		os.Exit(0)
	}

	check := func(label string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		t0 := time.Now()
		c, err := busdial.Raw(ctx, *mode, *busPath)
		if err != nil {
			fmt.Printf("[%s] bus connect (%s %s): %v after %s -> verdict %s\n", label, *mode, *busPath, err, time.Since(t0), classifyBusErr(err))
			return
		}
		defer c.Close()
		fmt.Printf("[%s] bus connected (%s) unixfd=%v\n", label, *mode, c.SupportsUnixFDs())
		var p dbus.ObjectPath
		var name string
		var invb []byte
		err = call(ctx, c, "GetUnitByPIDFD", dbus.UnixFD(pfd)).Store(&p, &name, &invb)
		if err != nil {
			fmt.Printf("[%s] GetUnitByPIDFD: %v -> %s\n", label, err, classifyBusErr(err))
		} else {
			fmt.Printf("[%s] GetUnitByPIDFD: path=%s unit=%s invocation=%s\n", label, p, name, hex.EncodeToString(invb))
		}
		var p2 dbus.ObjectPath
		err = call(ctx, c, "GetUnitByPID", uint32(*pid)).Store(&p2)
		if err != nil {
			fmt.Printf("[%s] GetUnitByPID(%d): %v -> %s\n", label, *pid, err, classifyBusErr(err))
		} else {
			id, iv, err := unitInvocation(ctx, c, p2)
			fmt.Printf("[%s] GetUnitByPID(%d): path=%s Id=%s InvocationID=%s err=%v\n", label, *pid, p2, id, iv, err)
		}
		if *unit != "" {
			var p3 dbus.ObjectPath
			if err := call(ctx, c, "GetUnit", *unit).Store(&p3); err != nil {
				fmt.Printf("[%s] GetUnit(%s): %v -> %s\n", label, *unit, err, classifyBusErr(err))
			} else {
				id, iv, _ := unitInvocation(ctx, c, p3)
				var mp dbus.Variant
				_ = c.Object("org.freedesktop.systemd1", p3).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.systemd1.Service", "MainPID").Store(&mp)
				fmt.Printf("[%s] GetUnit(%s): Id=%s InvocationID=%s MainPID=%v\n", label, *unit, id, iv, mp.Value())
			}
		}
		if *inv != "" {
			if err == nil && name == *unit && hex.EncodeToString(invb) == *inv {
				fmt.Printf("[%s] verdict Alive(same unit+invocation, pidfd-pinned)\n", label)
			}
		}
	}
	check("t0")
	if *hold > 0 {
		fmt.Printf("holding pidfd %s ...\n", *hold)
		time.Sleep(*hold)
		fds := []unix.PollFd{{Fd: int32(pfd), Events: unix.POLLIN}}
		n, _ := unix.Poll(fds, 0)
		fmt.Printf("poll(pidfd) readable=%v (readable == process exited)\n", n == 1)
		err := unix.PidfdSendSignal(pfd, 0, nil, 0)
		fmt.Printf("pidfd_send_signal(0) on held fd: %v\n", err)
		st2, err2 := startTicks(*pid)
		fmt.Printf("/proc/%d/stat now: ticks=%v err=%v\n", *pid, st2, err2)
		check("t1")
	}
}
