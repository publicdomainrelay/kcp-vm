//go:build release

package tsbundle

import "github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"

func Select(vals clicfg.Values, starts ...string) (Source, error) {
	return Embedded{FS: closureFS, Dir: closureDir}, nil
}

func Describe() Info { return Embedded{FS: closureFS, Dir: closureDir}.Info() }
