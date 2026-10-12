package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestimage"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const (
	statePresent = "present"
	stateBuilt   = "built"
	stateReused  = "reused"
	stateAbsent  = "absent"
)

type ensureResult struct {
	State string `json:"state"`

	Fingerprint string `json:"fingerprint"`

	Dir string `json:"dir"`

	Config string `json:"config"`

	ReusedFrom string `json:"reusedFrom,omitempty"`

	Kernel string `json:"kernel,omitempty"`

	Initramfs string `json:"initramfs,omitempty"`

	Rootfs string `json:"rootfs,omitempty"`

	RootfsMiB int64 `json:"rootfsMiB,omitempty"`

	KernelSource string `json:"kernelSource,omitempty"`

	KernelConfig string `json:"kernelConfig,omitempty"`

	ModuleSource string `json:"moduleSource,omitempty"`

	KernelOrigin string `json:"kernelOrigin,omitempty"`

	KernelVersion string `json:"kernelVersion,omitempty"`
}

func rootfsTemplateMiB(dir string) int64 {
	info, err := os.Stat(filepath.Join(dir, nodefirecracker.RootfsImageName))
	if err != nil {
		return 0
	}
	return info.Size() >> 20
}

func buildTheImageTheConfigurationNames(
	cfg Config,
	result *ensureResult,
	fingerprint *string,
	dir *string,
	present *bool,
) error {
	if err := buildInit(cfg); err != nil {
		return err
	}
	named, err := nodefirecracker.Fingerprint(cfg.Path, buildConfigOf(cfg))
	if err != nil {
		return err
	}
	*fingerprint = named
	*dir = storeDir(cfg, named)
	result.Fingerprint = named
	result.Dir = *dir
	whole, err := completeImage(cfg, named)
	if err != nil {
		return err
	}
	*present = whole
	if whole {
		result.State = statePresent
		return nil
	}
	artifacts, err := publishImage(cfg, named)
	if err != nil {
		return err
	}
	result.State = stateBuilt
	result.Kernel = artifacts.Kernel
	result.Initramfs = artifacts.Initramfs
	result.Rootfs = artifacts.Rootfs
	return nil
}

func Ensure(configPath string, reuseStale bool, asJSON bool, preinstallPath string, presentOnly bool, initAsset string) error {
	if configPath == "" {
		return fmt.Errorf("-config is required: the name of the image to ask for is derived from the " +
			"inputs a configuration reads, and there is no configuration here to derive it from")
	}
	if gc, ok, err := guestImageConfigAt(configPath); err != nil {
		return err
	} else if ok {
		return ensureGuestAndReport(configPath, gc, asJSON, preinstallPath, presentOnly)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := cfg.overrideInitAsset(initAsset); err != nil {
		return err
	}
	if strings.TrimSpace(preinstallPath) != "" {
		absolute, err := filepath.Abs(preinstallPath)
		if err != nil {
			return fmt.Errorf("socialweb-nodeimage: resolving the preinstall manifest at %s: %w -- the "+
				"manifest is an input of the image and an input whose name changes with the working "+
				"directory is one the recorded inputs would hold the image to the wrong file for",
				preinstallPath, err)
		}
		cfg.Preinstall = absolute
	}
	fingerprint, err := nodefirecracker.Fingerprint(cfg.Path, buildConfigOf(cfg))
	if err != nil {
		return fmt.Errorf("cannot tell which image under %s the configuration at %s names, so there "+
			"is no name to ask for and nothing has been built, reused or pulled: %w",
			cfg.OutDir, cfg.Path, err)
	}
	dir := storeDir(cfg, fingerprint)
	present, err := completeImage(cfg, fingerprint)
	if err != nil {
		return err
	}
	result := ensureResult{
		State:         statePresent,
		Fingerprint:   fingerprint,
		Dir:           dir,
		Config:        cfg.Path,
		KernelSource:  cfg.KernelSource,
		KernelConfig:  cfg.KernelConfig,
		ModuleSource:  cfg.ModuleSource,
		KernelOrigin:  cfg.KernelOrigin,
		KernelVersion: cfg.KernelVersion,
	}
	if presentOnly && !present {
		result.State = stateAbsent
		if asJSON {
			raw, err := json.Marshal(result)
			if err != nil {
				return fmt.Errorf("socialweb-nodeimage: rendering the result of asking whether %s holds "+
					"the image the configuration at %s names: %w", dir, cfg.Path, err)
			}
			fmt.Printf("%s\n", raw)
		} else {
			fmt.Printf("absent %s\n  config   %s\n  nothing built, nothing pulled\n", dir, cfg.Path)
		}
		return refusedAbsent(cfg, fingerprint, dir)
	}
	switch {
	case present:
	case reuseStale:
		entry, err := newestWholeImage(cfg)
		if err != nil {
			return err
		}
		if entry.path == "" {
			return refusedNothingToReuse(cfg, fingerprint)
		}
		result.State = stateReused
		result.Dir = entry.path
		result.ReusedFrom = entry.fingerprint
	default:
		if err := withStageDirHeld(cfg, func() error {
			return buildTheImageTheConfigurationNames(cfg, &result, &fingerprint, &dir, &present)
		}); err != nil {
			return err
		}
	}
	if err := refusePreinstallTheImageDoesNotCarry(cfg, result.Dir); err != nil {
		return err
	}
	result.RootfsMiB = rootfsTemplateMiB(result.Dir)
	if asJSON {
		raw, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("socialweb-nodeimage: rendering the result of asking whether %s holds "+
				"the image the configuration at %s names: %w", dir, cfg.Path, err)
		}
		fmt.Printf("%s\n", raw)
		return nil
	}
	switch result.State {
	case statePresent:
		fmt.Printf("up to date %s\n  config   %s\n  nothing built, nothing pulled\n", dir, cfg.Path)
	case stateReused:
		fmt.Printf("reused %s\n  config   %s\n  asked for %s\n  nothing built, nothing pulled\n",
			result.Dir, cfg.Path, fingerprint)
	default:
		fmt.Printf("image %s\n  kernel    %s\n  initramfs %s\n  rootfs    %s\n",
			dir, result.Kernel, result.Initramfs, result.Rootfs)
	}
	return nil
}

func refusedAbsent(cfg Config, fingerprint string, dir string) error {
	return fmt.Errorf("socialweb-nodeimage: -present-only asked whether the store at %s already holds the "+
		"image the inputs of the configuration at %s name, and it does not: the inputs name %s and %s "+
		"is not a whole build of them. Nothing was built, pulled or reused, and this is the answer "+
		"rather than a failure: a host that must not build -- or must not build as the user it is "+
		"running the caller as -- asks this question so that it can decide whether to build, and an "+
		"absent answer that returned success would be indistinguishable from a whole image in the "+
		"store. Build it (socialweb-nodeimage -ensure -config %s) or point -config at the "+
		"configuration the images in %s were built from", cfg.OutDir, cfg.Path, fingerprint, dir,
		cfg.Path, cfg.OutDir)
}

func refusePreinstallTheImageDoesNotCarry(cfg Config, dir string) error {
	manifest, err := nodefirecracker.LoadManifest(dir)
	if err != nil {
		return err
	}
	if manifest.Inputs == nil {
		return nodefirecracker.ErrNoBuildInputs
	}
	recorded := strings.TrimSpace(manifest.Inputs.Preinstall)
	asked := strings.TrimSpace(cfg.Preinstall)
	switch {
	case recorded == "" && asked != "":
		return fmt.Errorf("socialweb-nodeimage: the image in %s records no preinstall, and the "+
			"preinstall manifest %s was named for this run. The manifest is an input of the image -- "+
			"it is read and digested into the name the image is published under -- so an image built "+
			"without it and an image built with it are two different images, and this is the one built "+
			"without it: none of the software %s bakes in is in it. A guest booted from it comes up, "+
			"joins and reaches Running carrying none of what the manifest exists to install, and "+
			"nothing in the guest or in the cluster names the image as the cause, so this is refused "+
			"rather than used. Build the image these inputs name (socialweb-nodeimage -ensure -config "+
			"%s -preinstall %s)", dir, asked, asked, cfg.Path, asked)
	case recorded != "" && asked == "":
		return fmt.Errorf("socialweb-nodeimage: the image in %s records the preinstall manifest %s "+
			"and this run named no preinstall at all. The manifest is an input of the image -- it is "+
			"read and digested into the name the image is published under -- so an image built with it "+
			"and an image built without it are two different images, and this is the one built with "+
			"the software %s bakes in: the inputs in front of this run do not name that software, and "+
			"an image carrying a package list nobody named is one whose contents cannot be derived "+
			"from its inputs. Refused rather than used: build the image these inputs name "+
			"(socialweb-nodeimage -ensure -config %s), or name the manifest this image was built with "+
			"(-preinstall %s)", dir, recorded, recorded, cfg.Path, recorded)
	}
	return nil
}

func refusedNothingToReuse(cfg Config, fingerprint string) error {
	return fmt.Errorf("socialweb-nodeimage: -reuse-stale asked for an image already in the store, and "+
		"the store at %s holds none that this configuration could have produced. The inputs of the "+
		"configuration at %s name %s and nothing is published under it, and no other directory there "+
		"is both whole and built for the %s VMM. Booting the newest image in the store anyway would "+
		"be booting an image built from inputs this configuration does not describe, and that guest "+
		"comes up and reaches Running with nothing naming the input that moved, which is the failure "+
		"the name exists to prevent, so this is refused rather than guessed. Build it "+
		"(socialweb-nodeimage -ensure -config %s) or point -config at the configuration the images "+
		"in %s were built from", cfg.OutDir, cfg.Path, fingerprint, cfg.VMMName(), cfg.Path, cfg.OutDir)
}

func ensureGuestAndReport(configPath string, gc guestImageConfig, asJSON bool, preinstallPath string, presentOnly bool) error {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	pin, err := guestimage.PinFor(gc.Target)
	if err != nil {
		return err
	}
	result, err := ensureGuest(absolute, gc, pin, preinstallPath, presentOnly)
	if err != nil && result.State != stateAbsent {
		return err
	}
	if asJSON {
		raw, merr := json.Marshal(result)
		if merr != nil {
			return fmt.Errorf("socialweb-nodeimage: %w", merr)
		}
		fmt.Printf("%s\n", raw)
	} else {
		fmt.Printf("%s %s\n  target   %s\n  source   %s\n", result.State, result.Dir, gc.Target, gc.Source)
	}
	return err
}
