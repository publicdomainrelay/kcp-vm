package nodefirecracker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func LoadImage(outDir, configPath string) (Image, Manifest, error) {
	cfg, err := LoadBuildConfig(configPath)
	if err != nil {
		return Image{}, Manifest{}, err
	}
	fingerprint, err := Fingerprint(configPath, cfg)
	if err != nil {
		return Image{}, Manifest{}, fmt.Errorf("nodefirecracker: naming the image the "+
			"configuration at %s asks for. That name is derived here, from the inputs the "+
			"configuration reads, and it is never taken from the configuration itself: an operator "+
			"who computed it by hand and pasted it into a setting would be the one deciding which "+
			"image a node boots, and every way that goes wrong is silent, because a stale "+
			"fingerprint names a directory that exists and validates -- so a node boots from an old "+
			"image and nothing says so, which is the failure the fingerprint exists to prevent: %w",
			configPath, err)
	}
	dir := filepath.Join(outDir, fingerprint)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Image{}, Manifest{}, fmt.Errorf("nodefirecracker: the store at %s holds nothing "+
				"under %s, which is the name the inputs of the configuration at %s give. Nothing "+
				"has been looked for anywhere else and no other image is booted in its place: an "+
				"image is published under the hash of what a build read and only a build writes "+
				"that name, so a configuration that changed since the last build names a directory "+
				"no build has taken. Booting the newest image in the store instead would be booting "+
				"the image from before the change, and that guest comes up, joins and reaches "+
				"Running with nothing anywhere naming the file that moved. Build it: "+
				"socialweb-nodeimage -config %s", outDir, fingerprint, configPath, configPath)
		}
		return Image{}, Manifest{}, fmt.Errorf("nodefirecracker: reading %s, the directory the "+
			"inputs of the configuration at %s name: %w -- an image that cannot be looked for "+
			"cannot be shown to be the one this configuration built", dir, configPath, err)
	}
	manifest, err := LoadManifest(dir)
	if err != nil {
		return Image{}, Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Image{}, Manifest{}, err
	}
	recorded, err := manifest.Inputs.Fingerprint()
	if err != nil {
		return Image{}, Manifest{}, err
	}
	if recorded != fingerprint {
		return Image{}, Manifest{}, fmt.Errorf("nodefirecracker: %s carries the name %s and the "+
			"record inside it hashes to %s. The name is the hash of what a build read and the "+
			"record is what that build wrote, so a directory whose name and record disagree was "+
			"not put there by a build of the configuration in front of this one. Renaming an image "+
			"into a name another configuration gives is the one way left to boot a node from an "+
			"image its configuration does not describe, and nothing downstream would report it: "+
			"the guest comes up, joins, and its phase reaches Running. It is refused rather than "+
			"repaired, because a build is what makes a name and an image true together and nothing "+
			"here can tell which of the two is the wrong one", dir, fingerprint, recorded)
	}
	if err := manifest.CheckInputs(); err != nil {
		return Image{}, Manifest{}, err
	}
	image := Image{
		Kernel:    filepath.Join(dir, VMLinuxName),
		Initramfs: filepath.Join(dir, InitramfsName),
		Rootfs:    filepath.Join(dir, RootfsImageName),
	}
	if err := image.Validate(); err != nil {
		return Image{}, Manifest{}, err
	}
	return image, manifest, nil
}
