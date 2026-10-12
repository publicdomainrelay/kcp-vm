package hostsvc

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrNoListener = errors.New("hostsvc: the process holds no listening TCP socket yet")

var ErrManyListeners = errors.New("hostsvc: the process holds more than one listening TCP socket")

func WaitListenPort(ctx context.Context, pid int, every time.Duration) (int, error) {
	for {
		port, err := ListenPort(pid)
		if !errors.Is(err, ErrNoListener) {
			return port, err
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("hostsvc: pid %d bound no port before %w", pid, ctx.Err())
		case <-time.After(every):
		}
	}
}
