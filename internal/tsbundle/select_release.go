//go:build release

package tsbundle

import (
	"github.com/johnandersen777/socialweb-computer-kcp/internal/buildstamp"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
)

func embedded() Embedded { return Embedded{FS: closureFS, Dir: closureDir} }

func workingTree() (WorkingTree, bool) {
	if !buildstamp.Dev() || buildstamp.SourceRoot == "" || !isCheckout(buildstamp.SourceRoot) {
		return WorkingTree{}, false
	}
	return WorkingTree{Dir: buildstamp.SourceRoot, Closure: embedded()}, true
}

func Select(vals clicfg.Values, starts ...string) (Source, error) {
	if w, ok := workingTree(); ok {
		return w, nil
	}
	return embedded(), nil
}

func Describe() Info {
	if w, ok := workingTree(); ok {
		return w.Info()
	}
	return embedded().Info()
}
