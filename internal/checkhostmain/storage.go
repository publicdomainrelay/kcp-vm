package checkhostmain

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const imageApparentBytes = 2300 << 20

func imageSize(dir string) (int64, bool) {
	if _, err := nodefirecracker.LoadManifest(dir); err != nil {
		return 0, false
	}
	total := int64(0)
	for _, name := range []string{
		nodefirecracker.VMLinuxName,
		nodefirecracker.InitramfsName,
		nodefirecracker.RootfsImageName,
	} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return 0, false
		}
		total += info.Size()
	}
	return total, true
}

func largestImage(outDir string) int64 {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return 0
	}
	largest := int64(0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		size, ok := imageSize(filepath.Join(outDir, entry.Name()))
		if ok && size > largest {
			largest = size
		}
	}
	return largest
}

func freeSpace(path string) (int64, string, error) {
	existing := path
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return 0, "", fmt.Errorf("no part of %s exists", path)
		}
		existing = parent
	}
	free, err := freeSpaceBytes(existing)
	if err != nil {
		return 0, "", err
	}
	return free, existing, nil
}

func diskResult(f facts) Result {
	if f.outDir == "" {
		return Result{
			Label: "host.disk",
			Kind:  f.absent(),
			Detail: f.notHere("the output path whose free space matters") + fmt.Sprintf("no config in "+
				"hand names out_dir and %s is unset. A build writes an image of about %s into that "+
				"path and the store keeps several of them, so the room is a property of the path: "+
				"name it, or mount one in", envOutDir, gib(imageApparentBytes)),
		}
	}
	free, where, err := freeSpace(f.outDir)
	if err != nil {
		return Result{
			Label: "host.disk",
			Kind:  Missing,
			Detail: fmt.Sprintf("the free space on the output path %s cannot be read: %v. A build "+
				"that runs out mid-write leaves a staging directory nothing asks for, and the store "+
				"reader will not recognise it, so the failure looks like a missing image rather than "+
				"a full disk", f.outDir, err),
		}
	}
	imageBytes := largestImage(f.outDir)
	measured := true
	if imageBytes == 0 {
		imageBytes = imageApparentBytes
		measured = false
	}
	budget := int64(f.retain) * imageBytes
	detail := fmt.Sprintf("%s free on %s; an image is %s of payload (vmlinux + initramfs.cpio + "+
		"rootfs.ext4)", gib(free), where, gib(imageBytes))
	if !measured {
		detail += ", measured from this host's own images when the store holds one and assumed " +
			"otherwise"
	}
	if free < budget {
		return Result{
			Label: "host.disk",
			Kind:  Missing,
			Detail: fmt.Sprintf("%s. The store is kept at %d images, so this path wants %s and has "+
				"%s -- room for %d images and it holds %d. mkfs.ext4 writes the root filesystem whole "+
				"into that path and the staging tree is copied beside it, so a build that starts here "+
				"fails partway through with a write error, leaves a .building directory nothing asks "+
				"for, and reports nothing that names the disk", detail, f.retain, gib(budget),
				gib(free), free/imageBytes, storeCount(f.outDir)),
		}
	}
	return Result{
		Label: "host.disk",
		Kind:  Holds,
		Detail: fmt.Sprintf("%s. The store is kept at %d images, so this path wants %s of it, has "+
			"room for %d and holds %d: the first figure is a projection from the free space and the "+
			"second is what is in the store now, and they are counted differently on purpose -- a "+
			"budget is not an inventory", detail, f.retain, gib(budget), free/imageBytes,
			storeCount(f.outDir)),
	}
}

func storeCount(outDir string) int {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := imageSize(filepath.Join(outDir, entry.Name())); ok {
			n++
		}
	}
	return n
}
