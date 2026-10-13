package diskusage

import (
	"io/fs"
	"path/filepath"
)

type Usage struct {
	Bytes int64
	Files int64
}

func Of(root string) (Usage, error) {
	var u Usage
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if d == nil {
				return err
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		u.Bytes += allocated(fi)
		if !d.IsDir() {
			u.Files++
		}
		return nil
	})
	return u, err
}
