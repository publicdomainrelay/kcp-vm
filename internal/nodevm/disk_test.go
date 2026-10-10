package nodevm

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"testing"
)

func TestCloneFileCopiesAMutableRootfsAndKeepsItPrivate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "image-rootfs.ext4")
	body := make([]byte, 1<<20)
	for i := range body {
		body[i] = byte(i)
	}
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatalf("staging the image's rootfs: %v", err)
	}

	dst := filepath.Join(dir, "node-rootfs.ext4")
	if err := CloneFile(src, dst); err != nil {
		t.Fatalf("CloneFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading the clone: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("the clone is %d bytes and the image's rootfs is %d, so the node's copy is not "+
			"the image's", len(got), len(body))
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat the clone: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("the clone is %v, want 0600: from the guest's first boot it holds the join "+
			"token at /kcp/token and in k3s's node-password files", st.Mode().Perm())
	}

	if err := os.WriteFile(dst, []byte("a previous boot's state"), 0o600); err != nil {
		t.Fatalf("staging a stale clone: %v", err)
	}
	if err := CloneFile(src, dst); err != nil {
		t.Fatalf("CloneFile over a stale clone: %v", err)
	}
	again, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading the re-clone: %v", err)
	}
	if string(again) != string(body) {
		t.Fatal("a second CloneFile left the previous node's state under the new node's name, " +
			"which is one guest's k3s state read by the next guest's boot")
	}
}

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
