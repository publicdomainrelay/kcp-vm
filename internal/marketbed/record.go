package marketbed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const RecordPath = "deploy/denokcp/vendor.json"

var VendoredDirs = []string{"deploy/denokcp", "deploy/examples/atproto/market"}

type Record struct {
	Repository string          `json:"repository"`
	Ref        string          `json:"ref"`
	Commit     string          `json:"commit"`
	KcpLibs    string          `json:"kcpLibsCommit"`
	Fork       json.RawMessage `json:"fork"`
	Files      []VendoredFile  `json:"files"`
	Local      []LocalFile     `json:"local"`
}

type VendoredFile struct {
	Path     string `json:"path"`
	Upstream string `json:"upstream"`
	SHA256   string `json:"sha256"`
	Adapted  string `json:"adapted,omitempty"`
}

type LocalFile struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

func LoadRecord(repoRoot string) (Record, error) {
	var r Record
	raw, err := os.ReadFile(filepath.Join(repoRoot, RecordPath))
	if err != nil {
		return r, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("%s: %w", RecordPath, err)
	}
	if r.Commit == "" || r.Repository == "" {
		return r, fmt.Errorf("%s names no repository or commit", RecordPath)
	}
	return r, nil
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func FileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Digest(data), nil
}

func ListVendoredTree(repoRoot string) ([]string, error) {
	var out []string
	for _, dir := range VendoredDirs {
		base := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func FindDenoKcp(repoRoot string) (string, bool) {
	if dir := os.Getenv("DENO_KCP_DIR"); dir != "" {
		return dir, isDir(filepath.Join(dir, ".git")) || isFile(filepath.Join(dir, ".git"))
	}
	dir := repoRoot
	for {
		candidate := filepath.Join(dir, "deno-kcp")
		if isFile(filepath.Join(candidate, "go.mod")) &&
			(isDir(filepath.Join(candidate, ".git")) || isFile(filepath.Join(candidate, ".git"))) {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

var ErrCommitAbsent = errors.New("commit not present in the deno-kcp checkout")

func UpstreamFile(denoKcpDir, commit, path string) ([]byte, error) {
	probe := exec.Command("git", "-C", denoKcpDir, "cat-file", "-e", commit+"^{commit}")
	if err := probe.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s in %s", ErrCommitAbsent, commit, denoKcpDir)
	}
	cmd := exec.Command("git", "-C", denoKcpDir, "show", commit+":"+path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s: %v: %s", commit, path, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
