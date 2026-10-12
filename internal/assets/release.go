//go:build release

package assets

import (
	"fmt"
	"io/fs"
	"strings"
)

func Release() bool { return true }

func Platform() string { return platform }

func Ready() error {
	pins, err := platformPins(platform)
	if err != nil {
		return err
	}
	if missing := MissingBlobs(pins, blobs, blobDir); len(missing) > 0 {
		return fmt.Errorf("release build for %s is missing embedded assets %s; it refuses to start (stage them with scripts/stage-assets.sh, then rebuild with -tags release)",
			platform, strings.Join(missing, ", "))
	}
	return nil
}

func Resolve(opts Options, executable string) (Exec, error) {
	if err := Ready(); err != nil {
		return Exec{}, err
	}
	pins, err := platformPins(platform)
	if err != nil {
		return Exec{}, err
	}
	pin, ok := PinFor(pins, executable)
	if !ok {
		return Exec{}, fmt.Errorf("assets: no embedded asset carries %q on %s", executable, platform)
	}
	if opts.Cache == "" {
		return Exec{}, fmt.Errorf("assets: asset-cache is unset; install points it at <state>/runtime, dev at the per-user cache")
	}
	return Store{Root: opts.Cache, Log: opts.Log}.Resolve(pin, FSBlobs(blobs, blobDir), executable)
}

func embeddedBlob(name string) bool {
	_, err := fs.Stat(blobs, blobDir+"/"+BlobName(name))
	return err == nil
}
