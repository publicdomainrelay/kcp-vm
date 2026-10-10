package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const (
	statePresent = "present"
	stateBuilt   = "built"
	stateReused  = "reused"
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
}

func Ensure(configPath string, reuseStale bool, asJSON bool, preinstallPath string) error {
	if configPath == "" {
		return fmt.Errorf("-config is required: the name of the image to ask for is derived from the " +
			"inputs a configuration reads, and there is no configuration here to derive it from")
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
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
	if err := buildAgent(cfg); err != nil {
		return err
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
	result := ensureResult{State: statePresent, Fingerprint: fingerprint, Dir: dir, Config: cfg.Path}
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
		artifacts, err := publishImage(cfg, fingerprint)
		if err != nil {
			return err
		}
		result.State = stateBuilt
		result.Kernel = artifacts.Kernel
		result.Initramfs = artifacts.Initramfs
		result.Rootfs = artifacts.Rootfs
	}
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
