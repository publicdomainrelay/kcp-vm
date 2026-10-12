package nodevm

import (
	"os"
	"path/filepath"
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
