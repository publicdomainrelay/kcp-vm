package tsbundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
)

const (
	KindEmbedded  = "embedded"
	KindOrgRoot   = "org-root"
	OrgRootEnv    = "SOCIALWEB_COMPUTER_ORG_ROOT"
	OrgRootOption = "org-root"
)

var SiblingRepos = []string{"typescript-helpers", "atproto-market", "socialweb-computer-kcp"}

type Prepared struct {
	Root     string       `json:"root"`
	Key      string       `json:"key"`
	Mode     denorun.Mode `json:"mode"`
	DenoDirs string       `json:"denoDirs"`
}

func (p Prepared) Spec(deno string, e EntrySpec, args []string) denorun.Spec {
	dir, en := e.At(p.Root)
	return denorun.Spec{Deno: deno, Mode: p.Mode, DenoDirs: p.DenoDirs, Key: p.Key, Dir: dir, Entry: en, Args: args}
}

type Source interface {
	Kind() string
	Production() bool
	Prepare(stateDir string) (Prepared, error)
}

type OrgRoot struct {
	Dir string
}

func (OrgRoot) Kind() string { return KindOrgRoot }

func (OrgRoot) Production() bool { return false }

func (o OrgRoot) Prepare(stateDir string) (Prepared, error) {
	root, err := CheckOrgRoot(o.Dir)
	if err != nil {
		return Prepared{}, err
	}
	state, err := absState(stateDir)
	if err != nil {
		return Prepared{}, err
	}
	key, err := denorun.KeyForPath(root)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Root: root, Key: key, Mode: denorun.Dev, DenoDirs: filepath.Join(state, "deno")}, nil
}

func absState(stateDir string) (string, error) {
	if stateDir == "" {
		return "", errors.New("tsbundle: no state directory")
	}
	return filepath.Abs(stateDir)
}

type candidate int

const (
	notOrgRoot candidate = iota
	isOrgRoot
)

func inspect(dir string) (candidate, error) {
	var symlinked, other []string
	for _, name := range SiblingRepos {
		st, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return notOrgRoot, nil
		}
		switch {
		case st.Mode()&fs.ModeSymlink != 0:
			symlinked = append(symlinked, name)
		case !st.IsDir():
			other = append(other, name)
		}
	}
	if len(symlinked) > 0 {
		return notOrgRoot, fmt.Errorf("tsbundle: org root %s holds %s as a symlink; a symlinked sibling is not a directory to Deno.readDir, so it is refused (check it out as a real directory or a worktree)",
			dir, strings.Join(symlinked, ", "))
	}
	if len(other) > 0 {
		return notOrgRoot, fmt.Errorf("tsbundle: org root %s holds %s, which is not a directory", dir, strings.Join(other, ", "))
	}
	return isOrgRoot, nil
}

func CheckOrgRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	c, err := inspect(abs)
	if err != nil {
		return "", err
	}
	if c != isOrgRoot {
		return "", fmt.Errorf("tsbundle: %s is not an org root: it must hold %s as real directories", abs, strings.Join(SiblingRepos, ", "))
	}
	return abs, nil
}

func FindOrgRoot(starts ...string) (string, error) {
	var tried []string
	for _, start := range starts {
		if start == "" {
			continue
		}
		dir, err := filepath.Abs(start)
		if err != nil {
			return "", err
		}
		tried = append(tried, dir)
		for {
			c, err := inspect(dir)
			if err != nil {
				return "", err
			}
			if c == isOrgRoot {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", fmt.Errorf("tsbundle: no org root (a directory holding %s) above %s; set %s",
		strings.Join(SiblingRepos, ", "), strings.Join(tried, " or "), OrgRootEnv)
}

type Info struct {
	Kind         string `json:"kind"`
	Embedded     bool   `json:"embedded"`
	TreeSHA256   string `json:"treeSha256,omitempty"`
	ArchiveBytes int64  `json:"archiveBytes,omitempty"`
	Deno         string `json:"deno,omitempty"`
	Error        string `json:"error,omitempty"`
}

func (e Embedded) Info() Info {
	m, err := e.Manifest()
	if err != nil {
		return Info{Kind: KindEmbedded, Error: err.Error()}
	}
	return Info{Kind: KindEmbedded, Embedded: true, TreeSHA256: m.TreeSHA256, ArchiveBytes: m.ArchiveBytes, Deno: m.Deno}
}
