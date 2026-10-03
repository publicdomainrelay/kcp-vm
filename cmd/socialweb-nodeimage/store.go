package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const stagingName = ".building"

func stagingPattern(fingerprint string) string {
	return "." + fingerprint + stagingName + "-" + strconv.Itoa(os.Getpid()) + "-*"
}

func stagingOf(name string) (fingerprint string, pid int, ok bool) {
	rest, found := strings.CutPrefix(name, ".")
	if !found {
		return "", 0, false
	}
	at := strings.Index(rest, stagingName+"-")
	if at < 0 {
		return "", 0, false
	}
	fingerprint = rest[:at]
	if !nodefirecracker.IsFingerprint(fingerprint) {
		return "", 0, false
	}
	parts := strings.Split(rest[at+len(stagingName)+1:], "-")
	if len(parts) < 2 {
		return "", 0, false
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 {
		return "", 0, false
	}
	return fingerprint, pid, true
}

func processAlive(pid int) bool {
	alive, err := hostos.Alive(pid)
	return err == nil && alive
}

func storeDir(cfg Config, fingerprint string) string {
	return filepath.Join(cfg.OutDir, fingerprint)
}

func imageArtifacts(dir string) Artifacts {
	return Artifacts{
		Kernel:    filepath.Join(dir, nodefirecracker.VMLinuxName),
		Initramfs: filepath.Join(dir, nodefirecracker.InitramfsName),
		Rootfs:    filepath.Join(dir, nodefirecracker.RootfsImageName),
	}
}

func imageInDir(dir string) nodefirecracker.Image {
	artifacts := imageArtifacts(dir)
	return nodefirecracker.Image{
		Kernel:    artifacts.Kernel,
		Initramfs: artifacts.Initramfs,
		Rootfs:    artifacts.Rootfs,
	}
}

func completeImage(cfg Config, fingerprint string) (bool, error) {
	dir := storeDir(cfg, fingerprint)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("socialweb-nodeimage: reading %s, the directory the inputs of the "+
			"configuration at %s name: %w -- an image that cannot be looked for cannot be shown to "+
			"exist, and a build that ran anyway would publish over whatever that directory holds",
			dir, cfg.Path, err)
	}
	manifest, err := nodefirecracker.LoadManifest(dir)
	if err != nil {
		return false, refusedNotWhole(cfg, dir, err)
	}
	if err := manifest.Validate(); err != nil {
		return false, refusedNotWhole(cfg, dir, err)
	}
	recorded, err := manifest.Inputs.Fingerprint()
	if err != nil {
		return false, refusedNotWhole(cfg, dir, err)
	}
	if recorded != fingerprint {
		return false, refusedNotWhole(cfg, dir, fmt.Errorf("the record in %s hashes to %s, and the "+
			"directory holding it is named %s", filepath.Join(dir, nodefirecracker.ManifestName),
			recorded, fingerprint))
	}
	if err := imageInDir(dir).Validate(); err != nil {
		return false, refusedNotWhole(cfg, dir, err)
	}
	return true, nil
}

type NotWholeError struct {
	Dir string

	ConfigPath string

	Cause error
}

func (e *NotWholeError) Error() string {
	return fmt.Sprintf("socialweb-nodeimage: %s carries the name of the inputs of the configuration "+
		"at %s and does not hold a whole build of them: %v -- that name is the hash of what a build "+
		"read, and a build publishes by renaming a finished directory into it, so a directory under "+
		"it was not written by a build of this configuration that ran to the end. It is neither "+
		"consumed nor published over: reading it would be reading an image no build of this "+
		"configuration produced, and renaming over it would destroy whatever it holds. Remove it and "+
		"run this again", e.Dir, e.ConfigPath, e.Cause)
}

func (e *NotWholeError) Unwrap() error {
	return e.Cause
}

func refusedNotWhole(cfg Config, dir string, cause error) error {
	return &NotWholeError{Dir: dir, ConfigPath: cfg.Path, Cause: cause}
}

func publishImage(cfg Config, fingerprint string) (Artifacts, error) {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return Artifacts{}, fmt.Errorf("socialweb-nodeimage: creating the image store at %s: %w",
			cfg.OutDir, err)
	}
	staging, err := os.MkdirTemp(cfg.OutDir, stagingPattern(fingerprint))
	if err != nil {
		return Artifacts{}, fmt.Errorf("socialweb-nodeimage: creating a staging directory beside %s: "+
			"%w -- a build is published by renaming a finished directory into the name its inputs "+
			"give it, and the directory it is renamed from has to be on the same filesystem as that "+
			"name, or the rename is a copy and a name exists before the image under it does",
			cfg.OutDir, err)
	}
	defer os.RemoveAll(staging)
	staged := cfg
	staged.OutDir = staging
	if _, err := Build(staged); err != nil {
		return Artifacts{}, err
	}
	dir := storeDir(cfg, fingerprint)
	if err := os.Rename(staging, dir); err != nil {
		present, checkErr := completeImage(cfg, fingerprint)
		if checkErr == nil && present {
			fmt.Printf("published %s: another build of the same inputs finished first, and this "+
				"one's output is discarded\n", dir)
			return imageArtifacts(dir), nil
		}
		return Artifacts{}, fmt.Errorf("socialweb-nodeimage: renaming %s to %s: %w -- the rename is "+
			"what gives the image its name, and it is the only moment a name and the image under it "+
			"exist together, so a name that cannot be taken is a build that has to be run again "+
			"rather than an image that can be used from where it lies", staging, dir, err)
	}
	return imageArtifacts(dir), nil
}
