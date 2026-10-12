//go:build !release

package tsbundle

import (
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
)

func Select(vals clicfg.Values, starts ...string) (Source, error) {
	if dir := vals.String(SourceRootOption); dir != "" {
		root, err := CheckCheckout(dir)
		if err != nil {
			return nil, err
		}
		return Checkout{Dir: root}, nil
	}
	if len(starts) == 0 {
		if wd, err := os.Getwd(); err == nil {
			starts = append(starts, wd)
		}
		if exe, err := os.Executable(); err == nil {
			starts = append(starts, filepath.Dir(exe))
		}
	}
	root, err := FindCheckout(starts...)
	if err != nil {
		return nil, err
	}
	return Checkout{Dir: root}, nil
}

func Describe() Info { return Info{Kind: KindCheckout} }
