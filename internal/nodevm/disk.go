package nodevm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/instance"
)

const nodeDiskFillDir = "kcp-node-disk-fill"

func MintOrReadNodePassword(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		return string(raw), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("nodevm: reading this node's own node password at %s: %w -- "+
			"this credential is per node and unlike the join token; k3s refuses a password it did "+
			"not issue for that node name as a duplicate hostname", path, err)
	}
	password, err := instance.New(nil)
	if err != nil {
		return "", fmt.Errorf("nodevm: minting a per-node password for this node: %w", err)
	}
	if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
		return "", fmt.Errorf("nodevm: writing this node's own password to %s: %w -- it "+
			"is NOT the cluster join token and must never become one", path, err)
	}
	return string(password), nil
}

func BuildNodeDisk(path string, doc NodeDocument, sizeMiB int, stage func(string) error) error {
	if doc.NodeName == "" || doc.Token == "" || doc.NodePassword == "" || doc.Server == "" {
		return fmt.Errorf("%w: the disk this node is identified by would carry node name %q, server "+
			"%q, a token of %d bytes and a node password of %d bytes; a disk missing any of them "+
			"boots a guest that cannot join, and it fails as a guest that never appears rather "+
			"than as a disk that was built wrong", ErrIncomplete, doc.NodeName, doc.Server,
			len(doc.Token), len(doc.NodePassword))
	}
	if sizeMiB <= 0 {
		sizeMiB = DefaultDiskMiB
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("nodevm: encoding the identity of node %s: %w", doc.NodeName, err)
	}
	fill := filepath.Join(filepath.Dir(path), nodeDiskFillDir)
	if err := os.RemoveAll(fill); err != nil {
		return fmt.Errorf("nodevm: clearing %s: %w", fill, err)
	}
	if err := os.MkdirAll(fill, 0o700); err != nil {
		return fmt.Errorf("nodevm: creating %s: %w", fill, err)
	}
	defer os.RemoveAll(fill)
	if err := os.WriteFile(filepath.Join(fill, NodeFileName), raw, 0o600); err != nil {
		return fmt.Errorf("nodevm: writing the identity of node %s into %s: %w -- the "+
			"token is inside this file, so it is written 0600 and the image that carries it is too",
			doc.NodeName, fill, err)
	}
	if stage != nil {
		if err := stage(fill); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("nodevm: removing a previous disk at %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("nodevm: creating the disk at %s: %w", path, err)
	}
	if err := f.Truncate(int64(sizeMiB) << 20); err != nil {
		f.Close()
		return fmt.Errorf("nodevm: sizing the disk at %s to %d MiB: %w", path, sizeMiB, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("nodevm: closing the disk at %s: %w", path, err)
	}
	return mkfsExt4(path, fill, NodeDiskLabel)
}

func mkfsExt4(path, fill, label string) error {
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return fmt.Errorf("nodevm: mkfs.ext4 is not on this host's PATH, and it is what populates an "+
			"image file from a directory without mounting anything. This host is %s: on Linux it is "+
			"a package away, and on Windows it is not a system tool at all, so an operator running "+
			"the qemu substrate there has to provide an e2fsprogs toolchain (a WSL or Cygwin "+
			"mkfs.ext4 on PATH is enough) before an image can be built or a node placed: %w",
			hostos.Name(), err)
	}
	cmd := exec.Command("mkfs.ext4", "-q", "-F", "-d", fill, "-L", label, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nodevm: mkfs.ext4 -d %s %s: %v: %s", fill, path, err,
			trimOutput(out))
	}
	return nil
}

func CloneFile(src, dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("nodevm: removing a previous copy at %s: %w", dst, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("nodevm: opening %s to clone it for a node: %w -- every node needs its "+
			"own writable root filesystem, because two VMs writing one ext4 corrupt it and a shared "+
			"image carries one node's k3s state into the next node's boot", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("nodevm: creating the node's copy at %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("nodevm: copying %s to %s: %w -- every node needs its own writable root "+
			"filesystem, because two VMs writing one ext4 corrupt it and a shared image carries one "+
			"node's k3s state into the next node's boot", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("nodevm: closing the node's copy at %s: %w", dst, err)
	}
	return nil
}

func trimOutput(out []byte) string {
	const limit = 2048
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return string(out)
}
