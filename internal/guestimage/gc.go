package guestimage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

const (
	FetchPrefix   = ".fetch-"
	FetchKeep     = time.Hour
	BinaryTimeout = 30 * time.Second
)

var digestName = regexp.MustCompile(`^[0-9a-f]{64}$`)

func Names() ([]string, error) {
	g, err := versions.GuestImages()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range g.Targets {
		m, ok := t.Outputs[ManifestName]
		if !ok || !digestName.MatchString(m.SHA256) || t.Hypervisor == "" || t.Arch == "" {
			continue
		}
		out = append(out, filepath.Join(t.Hypervisor, t.Arch, m.SHA256))
	}
	sort.Strings(out)
	return out, nil
}

type binaryVersion struct {
	GuestImages []string `json:"guestImages"`
}

func NamedByBinary(ctx context.Context, binary string) ([]string, error) {
	if _, err := os.Stat(binary); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, BinaryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("guestimage: %s version --json: %w", binary, err)
	}
	var v binaryVersion
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("guestimage: %s version --json: %w", binary, err)
	}
	return v.GuestImages, nil
}

type GCKept struct {
	Dir    string `json:"dir"`
	Reason string `json:"reason"`
}

type GCReport struct {
	Root    string   `json:"root"`
	Removed []string `json:"removed"`
	Kept    []GCKept `json:"kept"`
}

func (s Store) Collect(keep map[string]string, now time.Time) (GCReport, error) {
	rep := GCReport{Root: s.Root, Removed: []string{}, Kept: []GCKept{}}
	if !filepath.IsAbs(s.Root) {
		return rep, fmt.Errorf("guestimage: the image store root %q is not absolute", s.Root)
	}
	top, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	for _, h := range top {
		dir := filepath.Join(s.Root, h.Name())
		if h.IsDir() && strings.HasPrefix(h.Name(), FetchPrefix) {
			if fi, err := h.Info(); err == nil && now.Sub(fi.ModTime()) > FetchKeep {
				if err := os.RemoveAll(dir); err != nil {
					return rep, err
				}
				rep.Removed = append(rep.Removed, dir)
			}
			continue
		}
		if !h.IsDir() {
			continue
		}
		arches, err := os.ReadDir(dir)
		if err != nil {
			return rep, err
		}
		for _, a := range arches {
			if !a.IsDir() {
				continue
			}
			adir := filepath.Join(dir, a.Name())
			images, err := os.ReadDir(adir)
			if err != nil {
				return rep, err
			}
			for _, img := range images {
				if !img.IsDir() || !digestName.MatchString(img.Name()) {
					continue
				}
				path := filepath.Join(adir, img.Name())
				if why, ok := keep[img.Name()]; ok {
					rep.Kept = append(rep.Kept, GCKept{Dir: path, Reason: why})
					continue
				}
				if err := os.RemoveAll(path); err != nil {
					return rep, err
				}
				rep.Removed = append(rep.Removed, path)
			}
		}
	}
	return rep, nil
}
