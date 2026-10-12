package testdirs

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func Short(t testing.TB) string {
	t.Helper()
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp(base, "swct-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
