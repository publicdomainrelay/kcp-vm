//go:build !unix

package diskusage

import "io/fs"

func allocated(fi fs.FileInfo) int64 { return fi.Size() }
