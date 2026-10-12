//go:build !linux

package vmruntime

func ForHost(cfg Config) (Runtime, error) { return nil, ErrUnsupported }
