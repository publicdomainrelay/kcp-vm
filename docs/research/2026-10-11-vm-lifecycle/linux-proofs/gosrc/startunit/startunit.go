package startunit

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

func Props(argv []string, extra map[string]string) ([]sd.Property, error) {
	flag := extra["X-PropExecStartBool"] == "true"
	delete(extra, "X-PropExecStartBool")
	props := []sd.Property{
		sd.PropDescription("swproof stand-in VM " + strings.Join(argv, " ")),
		sd.PropExecStart(argv, flag),
		{Name: "Type", Value: dbus.MakeVariant("exec")},
	}
	for k, v := range extra {
		switch k {
		case "Restart", "KillMode", "OOMPolicy", "ManagedOOMPreference", "CollectMode", "ManagedOOMMemoryPressure":
			props = append(props, sd.Property{Name: k, Value: dbus.MakeVariant(v)})
		case "MemoryMax", "MemorySwapMax", "TasksMax", "CPUQuotaPerSecUSec", "IOWeight", "MemoryHigh":
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return nil, err
			}
			props = append(props, sd.Property{Name: k, Value: dbus.MakeVariant(n)})
		case "AllowedCPUs":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, err
			}
			mask := make([]byte, n/8+1)
			mask[n/8] |= 1 << (n % 8)
			props = append(props, sd.Property{Name: k, Value: dbus.MakeVariant(mask)})
		default:
			return nil, fmt.Errorf("unsupported property %s", k)
		}
	}
	return props, nil
}

func Start(ctx context.Context, c *sd.Conn, unit string, argv []string, extra map[string]string) (string, error) {
	props, err := Props(argv, extra)
	if err != nil {
		return "", err
	}
	ch := make(chan string, 1)
	if _, err := c.StartTransientUnitContext(ctx, unit, "fail", props, ch); err != nil {
		return "", err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return "job-signal-timeout", ctx.Err()
	}
}
