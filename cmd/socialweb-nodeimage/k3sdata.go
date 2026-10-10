package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	k3sAssetMarker = `msg="Asset dir `

	k3sAssetData   = "data"
	k3sAssetSums   = ".sha256sums"
	k3sAssetLinks  = ".links"
	k3sAssetBin    = "bin"
	k3sAssetCNIDir = "cni"
	k3sAssetCNIBin = "cni"
)

func stagedK3sDir(cfg Config) string {
	return filepath.Join(cfg.StageDir, cfg.K3sDataDir)
}

func guestK3sDataDir(cfg Config) string {
	return filepath.Join(cfg.K3sDataDir, k3sAssetData)
}

func k3sAssetDir(cfg Config) (string, error) {
	cmd := exec.Command(cfg.K3sBinary, "--debug", "--data-dir", stagedK3sDir(cfg), "check-config", cfg.KernelConfig)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	dir, err := parseK3sAssetDir(out.String())
	if err != nil {
		return "", fmt.Errorf("socialweb-nodeimage: asking %s where it keeps its runtime assets: %w. "+
			"check-config's own verdict is about THIS host's running kernel and is not this build's "+
			"business -- what the build needs from the run is the directory k3s unpacked or found its "+
			"assets in, because a guest that has to unpack them itself does it on its first boot, in "+
			"front of the join it is being built to shorten (check-config exited %v)", cfg.K3sBinary, err, runErr)
	}
	return dir, nil
}

func parseK3sAssetDir(output string) (string, error) {
	found := ""
	for line := range strings.SplitSeq(output, "\n") {
		_, rest, ok := strings.Cut(line, k3sAssetMarker)
		if !ok {
			continue
		}
		dir, _, ok := strings.Cut(rest, `"`)
		if !ok || dir == "" {
			continue
		}
		if found != "" && found != dir {
			return "", fmt.Errorf("k3s named two asset directories, %s and %s, in one run, so which of "+
				"them the guest would boot from is decided by which line the reader happened to take", found, dir)
		}
		found = dir
	}
	if found == "" {
		return "", fmt.Errorf("k3s printed no %s line. Either this binary does not run on this host, or "+
			"it is a build whose extraction says nothing at debug level, and an image built on a guess "+
			"about where its assets are would be one whose guest unpacks them at every boot", strings.TrimSpace(k3sAssetMarker))
	}
	return found, nil
}

func stageK3sData(cfg Config) error {
	asset, err := k3sAssetDir(cfg)
	if err != nil {
		return err
	}
	return stageK3sAssets(cfg, asset)
}

func stageK3sAssets(cfg Config, asset string) error {
	dest := stagedK3sDir(cfg)
	name := filepath.Base(asset)
	staged := filepath.Join(dest, k3sAssetData, name)
	if !within(asset, dest) {
		if err := copyTree(asset, filepath.Join(dest, k3sAssetData)); err != nil {
			return fmt.Errorf("socialweb-nodeimage: staging k3s's runtime assets from %s into %s: %w -- "+
				"this is the directory k3s reported as its own, and staging it is what keeps the guest "+
				"from unpacking it into a filesystem it is already booting from", asset, staged, err)
		}
	}
	if err := verifyK3sAssets(staged); err != nil {
		return err
	}
	if err := linkK3sCurrent(cfg, dest, name); err != nil {
		return err
	}
	return linkK3sCNI(cfg, dest, name)
}

func verifyK3sAssets(staged string) error {
	if err := verifyHashes(filepath.Join(staged, k3sAssetBin)); err != nil {
		return fmt.Errorf("socialweb-nodeimage: the staged k3s runtime assets at %s: %w -- k3s checks "+
			"these same sums after it unpacks its own, so anything the build copies in has to pass them "+
			"here or the guest is booting a directory k3s would have refused", staged, err)
	}
	return verifyLinks(filepath.Join(staged, k3sAssetBin))
}

func verifyHashes(dir string) error {
	sums, err := readPairs(filepath.Join(dir, k3sAssetSums), 1, 0)
	if err != nil {
		return err
	}
	if len(sums) == 0 {
		return fmt.Errorf("%s lists no files", k3sAssetSums)
	}
	for name, want := range sums {
		got, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s is sha256 %s, not the %s %s lists for it", name, got, want, k3sAssetSums)
		}
	}
	return nil
}

func verifyLinks(dir string) error {
	links, err := readPairs(filepath.Join(dir, k3sAssetLinks), 0, 1)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return fmt.Errorf("%s lists no links", k3sAssetLinks)
	}
	for name, want := range links {
		got, err := os.Readlink(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s is not the link %s lists it as: %w", name, k3sAssetLinks, err)
		}
		if got != want {
			return fmt.Errorf("%s points at %s, not the %s %s lists", name, got, want, k3sAssetLinks)
		}
	}
	return nil
}

func readPairs(path string, key, val int) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	pairs := map[string]string{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pairs[fields[key]] = fields[val]
	}
	return pairs, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func linkK3sCurrent(cfg Config, dest, name string) error {
	path := filepath.Join(dest, k3sAssetData, "current")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("socialweb-nodeimage: clearing %s: %w", path, err)
	}
	target := filepath.Join(guestK3sDataDir(cfg), name)
	if err := os.Symlink(target, path); err != nil {
		return fmt.Errorf("socialweb-nodeimage: pointing %s at %s: %w -- k3s writes this link itself "+
			"when it unpacks, and the one it writes names the path on the machine that unpacked, which "+
			"inside the guest is a directory that does not exist", path, target, err)
	}
	return nil
}

func linkK3sCNI(cfg Config, dest, name string) error {
	binDir := filepath.Join(dest, k3sAssetData, name, k3sAssetBin)
	cniDir := filepath.Join(dest, k3sAssetData, k3sAssetCNIDir)
	if err := os.MkdirAll(cniDir, 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w", cniDir, err)
	}
	guestBin := filepath.Join(guestK3sDataDir(cfg), name, k3sAssetBin)
	guestCNI := filepath.Join(guestBin, k3sAssetCNIBin)
	if err := replaceSymlink(filepath.Join(cniDir, k3sAssetCNIBin), guestCNI); err != nil {
		return err
	}
	ents, err := os.ReadDir(binDir)
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: reading %s: %w", binDir, err)
	}
	for _, ent := range ents {
		path := filepath.Join(binDir, ent.Name())
		info, err := ent.Info()
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(path)
		if err != nil || target != k3sAssetCNIBin {
			continue
		}
		if err := replaceSymlink(filepath.Join(cniDir, ent.Name()), guestCNI); err != nil {
			return err
		}
	}
	return nil
}

func replaceSymlink(path, target string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("socialweb-nodeimage: clearing %s: %w", path, err)
	}
	if err := os.Symlink(target, path); err != nil {
		return fmt.Errorf("socialweb-nodeimage: pointing %s at %s: %w", path, target, err)
	}
	return nil
}
