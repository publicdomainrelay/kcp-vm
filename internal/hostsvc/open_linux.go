//go:build linux

package hostsvc

import "context"

func Open(ctx context.Context, cfg Config) (Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Backend == BackendProc {
		return openProc(ctx, cfg)
	}
	return openSystemd(ctx, cfg)
}
