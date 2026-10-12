package kcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

type RollMode string

const (
	ModeBlueGreen     RollMode = "blue-green"
	ModePlannedOutage RollMode = "planned-outage"
)

var ErrIncompatiblePair = errors.New("kcpserver: no storage-compatible kcpCompat record for this pair of kcp releases")

type Asset struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}

var releaseRE = regexp.MustCompile(`kcp-(v[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.-]*)`)

func Release(version string) string {
	if m := releaseRE.FindStringSubmatch(version); m != nil {
		return m[1]
	}
	return ""
}

func ReadAsset(ctx context.Context, path string) (Asset, error) {
	if !filepath.IsAbs(path) {
		return Asset{}, fmt.Errorf("kcpserver: kcp executable %q must be an absolute path", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Asset{}, err
	}
	f, err := os.Open(real)
	if err != nil {
		return Asset{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return Asset{}, fmt.Errorf("kcpserver: %s is not an executable file", real)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Asset{}, err
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(vctx, real, "--version").CombinedOutput()
	return Asset{Path: real, SHA256: hex.EncodeToString(h.Sum(nil)), Version: strings.TrimSpace(string(out))}, nil
}

type PairVerdict struct {
	Mode   RollMode            `json:"mode"`
	From   string              `json:"from"`
	To     string              `json:"to"`
	Reason string              `json:"reason"`
	Record *versions.KcpCompat `json:"record,omitempty"`
}

func CheckPair(from, to Asset, records []versions.KcpCompat, plannedOutage bool) (PairVerdict, error) {
	v := PairVerdict{From: Release(from.Version), To: Release(to.Version)}
	compatible := false
	switch {
	case from.SHA256 != "" && from.SHA256 == to.SHA256:
		compatible, v.Reason = true, "same asset"
	case v.From != "" && v.From == v.To:
		compatible, v.Reason = true, "same kcp release "+v.From+", a rebuilt binary"
	default:
		for i := range records {
			r := records[i]
			if (r.From == v.From && r.To == v.To) || (r.From == v.To && r.To == v.From) {
				v.Record = &r
				compatible = r.StorageCompatible
				v.Reason = fmt.Sprintf("kcpCompat %s -> %s storageCompatible=%t (diff sha256 %s)", r.From, r.To, r.StorageCompatible, r.DiffSHA256)
				break
			}
		}
		if v.Record == nil {
			v.Reason = fmt.Sprintf("no kcpCompat record for %q -> %q", v.From, v.To)
		}
	}
	if plannedOutage {
		v.Mode = ModePlannedOutage
		return v, nil
	}
	if !compatible {
		return v, fmt.Errorf("%w: %s; the only path is --planned-outage (pg_dump, stop the old slot, start the new one on the same store)", ErrIncompatiblePair, v.Reason)
	}
	v.Mode = ModeBlueGreen
	return v, nil
}
