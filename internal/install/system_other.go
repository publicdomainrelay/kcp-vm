//go:build !linux

package install

import (
	"context"
	"io"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
)

func NewSystem(out io.Writer) (System, error) { return nil, ErrUnsupported }

func SelfCheck(ctx context.Context, exe string, role hostcontract.Role, stateDir string) error {
	return ErrUnsupported
}

func RestartOwnUnits(ctx context.Context, units []string) error { return ErrUnsupported }

func NewStagedSystem(root string, out io.Writer) (System, error) { return nil, ErrUnsupported }

func OwnRuntimeDir() string { return "" }
