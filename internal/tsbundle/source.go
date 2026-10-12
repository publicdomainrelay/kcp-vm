package tsbundle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
)

const (
	KindEmbedded     = "embedded"
	KindCheckout     = "checkout"
	SourceRootEnv    = "SOCIALWEB_COMPUTER_SOURCE_ROOT"
	SourceRootOption = "source-root"
	RepoName         = "socialweb-computer-kcp"
	WorkspaceFile    = "deno.json"
	LockFile         = "deno.lock"
	EntriesFile      = "deploy/ts-entries.yaml"
	VendorDir        = "ts"
)

var checkoutMarkers = []string{WorkspaceFile, EntriesFile}

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

type Checkout struct {
	Dir string
}

func (Checkout) Kind() string { return KindCheckout }

func (Checkout) Production() bool { return false }

func (c Checkout) Prepare(stateDir string) (Prepared, error) {
	root, err := CheckCheckout(c.Dir)
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

func isCheckout(dir string) bool {
	for _, m := range checkoutMarkers {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(m)))
		if err != nil || !st.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func CheckCheckout(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !isCheckout(abs) {
		return "", fmt.Errorf("tsbundle: %s is not a %s checkout: it must hold %s", abs, RepoName, strings.Join(checkoutMarkers, " and "))
	}
	return abs, nil
}

func FindCheckout(starts ...string) (string, error) {
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
			if isCheckout(dir) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", fmt.Errorf("tsbundle: no %s checkout (a directory holding %s) above %s; set %s",
		RepoName, strings.Join(checkoutMarkers, " and "), strings.Join(tried, " or "), SourceRootEnv)
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
