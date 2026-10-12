//go:build linux

package nodevm

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func sparseSource(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.Write(make([]byte, 4096)); err != nil {
		t.Fatalf("writing the first block of %s: %v", path, err)
	}
	if _, err := f.Seek(size-4096, 0); err != nil {
		t.Fatalf("seeking in %s: %v", path, err)
	}
	if _, err := f.Write(make([]byte, 4096)); err != nil {
		t.Fatalf("writing the last block of %s: %v", path, err)
	}
}

func blocksOf(t *testing.T, path string) int64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.Blocks
}

func TestACloneOfASparseRootfsIsStillSparse(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "image-rootfs.ext4")
	sparseSource(t, src, 1<<30)
	source := blocksOf(t, src)
	if source*512 > 1<<20 {
		t.Skipf("this filesystem stored the sparse source in %d blocks, so it cannot show whether "+
			"a clone of it is sparse", source)
	}

	dst := filepath.Join(dir, "node-rootfs.ext4")
	if err := CloneFile(src, dst); err != nil {
		t.Fatalf("CloneFile: %v", err)
	}
	if blocks := blocksOf(t, dst); blocks*512 > 1<<20 {
		t.Fatalf("cloning a 1 GiB rootfs whose contents are 8 KiB wrote %d MiB, so every node "+
			"writes the whole image and the store's 2.3 GiB template becomes 2.3 GiB per guest: the "+
			"copy materialises the holes instead of copying the extents that hold data",
			blocks*512>>20)
	}
}
