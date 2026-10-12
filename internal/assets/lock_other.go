//go:build !unix

package assets

import "os"

func lockPath(p string) (func(), error) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	return func() { f.Close() }, nil
}
