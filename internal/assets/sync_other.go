//go:build !linux

package assets

import (
	"io/fs"
	"os"
	"path/filepath"
)

func syncTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		serr := f.Sync()
		if cerr := f.Close(); serr == nil {
			serr = cerr
		}
		return serr
	})
}
