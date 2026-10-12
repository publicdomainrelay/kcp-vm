package hostsvc

import (
	"context"
	"time"
)

const WatchPollInterval = 250 * time.Millisecond

type lister func(ctx context.Context, prefix string) ([]UnitStatus, error)

func unitKey(st UnitStatus) string {
	return string(st.State) + "|" + st.SubState + "|" + st.InvocationID + "|" + st.Result
}

func pollWatch(ctx context.Context, prefix string, list lister) (<-chan UnitEvent, error) {
	first, err := list(ctx, prefix)
	if err != nil {
		return nil, err
	}
	ch := make(chan UnitEvent, 64)
	seen := map[string]string{}
	for _, st := range first {
		seen[st.Name] = unitKey(st)
	}
	go func() {
		defer close(ch)
		t := time.NewTicker(WatchPollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			units, err := list(ctx, prefix)
			if err != nil {
				continue
			}
			now := map[string]string{}
			for _, st := range units {
				k := unitKey(st)
				now[st.Name] = k
				if seen[st.Name] != k {
					select {
					case ch <- UnitEvent{Name: st.Name, Status: st}:
					case <-ctx.Done():
						return
					}
				}
			}
			for name := range seen {
				if _, ok := now[name]; !ok {
					select {
					case ch <- UnitEvent{Name: name, Status: UnitStatus{Name: name, State: NotFound}}:
					case <-ctx.Done():
						return
					}
				}
			}
			seen = now
		}
	}()
	return ch, nil
}
