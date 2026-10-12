//go:build !release

package tsbundle

import (
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
)

func Select(vals clicfg.Values, starts ...string) (Source, error) {
	if dir := vals.String(OrgRootOption); dir != "" {
		root, err := CheckOrgRoot(dir)
		if err != nil {
			return nil, err
		}
		return OrgRoot{Dir: root}, nil
	}
	if len(starts) == 0 {
		if wd, err := os.Getwd(); err == nil {
			starts = append(starts, wd)
		}
		if exe, err := os.Executable(); err == nil {
			starts = append(starts, filepath.Dir(exe))
		}
	}
	root, err := FindOrgRoot(starts...)
	if err != nil {
		return nil, err
	}
	return OrgRoot{Dir: root}, nil
}

func Describe() Info { return Info{Kind: KindOrgRoot} }
