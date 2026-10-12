//go:build !linux

package hostsvc

import (
	"context"
	"fmt"
	"runtime"
)

func Open(ctx context.Context) (Manager, error) {
	return nil, fmt.Errorf("%w: unsupported on %s; see plan 0011/0012", ErrUnsupported, runtime.GOOS)
}
