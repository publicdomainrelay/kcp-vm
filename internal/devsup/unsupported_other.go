//go:build !linux

package devsup

import (
	"context"
	"fmt"
	"runtime"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

func Run(ctx context.Context, o Options) error {
	return fmt.Errorf("%w: devsup, the proc placement backend, is unsupported on %s; see plan 0011/0012", hostsvc.ErrUnsupported, runtime.GOOS)
}
