package denorun

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/versions"
)

type Mode string

const (
	Dev        Mode = "dev"
	Production Mode = "production"
)

const Executable = "deno"

var keyName = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Entry struct {
	Path     string   `json:"path" yaml:"path"`
	Preload  []string `json:"preload,omitempty" yaml:"preload,omitempty"`
	Unstable []string `json:"unstable,omitempty" yaml:"unstable,omitempty"`
	Config   string   `json:"config,omitempty" yaml:"config,omitempty"`
}

type Spec struct {
	Deno        string
	Mode        Mode
	DenoDirs    string
	Key         string
	Dir         string
	Entry       Entry
	Permissions []string
	Args        []string
}

type InstallOptions struct {
	Frozen      bool
	AllowImport []string
}

func PinnedVersion() string {
	all, err := versions.Assets()
	if err != nil {
		return ""
	}
	return all[Executable].Version
}

func CheckVersion(deno string) error {
	out, err := exec.Command(deno, "--version").Output()
	if err != nil {
		return fmt.Errorf("denorun: %s --version: %w", deno, err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	want := "deno " + strings.TrimPrefix(PinnedVersion(), "v") + " "
	if !strings.HasPrefix(first+" ", want) {
		return fmt.Errorf("denorun: %s is %q; versions.json pins %s", deno, first, PinnedVersion())
	}
	return nil
}

func Resolve(opts assets.Options) (assets.Exec, error) {
	return assets.Resolve(opts, Executable)
}

func KeyForPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(sum[:]), nil
}

func (s Spec) DenoDir() (string, error) {
	if !keyName.MatchString(s.Key) {
		return "", fmt.Errorf("denorun: DENO_DIR key %q is not a sha256; every DENO_DIR is keyed by the closure it serves", s.Key)
	}
	if !filepath.IsAbs(s.DenoDirs) {
		return "", fmt.Errorf("denorun: the DENO_DIR root %q is not absolute", s.DenoDirs)
	}
	return filepath.Join(s.DenoDirs, s.Key), nil
}

func (s Spec) check() error {
	if !filepath.IsAbs(s.Deno) {
		return fmt.Errorf("denorun: deno path %q is not absolute; deno is never found on PATH", s.Deno)
	}
	switch s.Mode {
	case Dev, Production:
	default:
		return fmt.Errorf("denorun: mode %q is neither dev nor production", s.Mode)
	}
	if s.Entry.Path == "" {
		return errors.New("denorun: no entry")
	}
	_, err := s.DenoDir()
	return err
}

func (s Spec) Argv() ([]string, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	argv := []string{s.Deno, "run", "--no-code-cache", "--node-modules-dir=none"}
	if s.Mode == Production {
		argv = append(argv, "--cached-only", "--frozen")
	}
	perms := s.Permissions
	if len(perms) == 0 {
		perms = []string{"-A"}
	}
	argv = append(argv, perms...)
	for _, u := range s.Entry.Unstable {
		argv = append(argv, "--unstable-"+u)
	}
	if s.Entry.Config != "" {
		argv = append(argv, "--config", s.Entry.Config)
	}
	for _, p := range s.Entry.Preload {
		argv = append(argv, "--preload", p)
	}
	argv = append(argv, s.Entry.Path)
	return append(argv, s.Args...), nil
}

func (s Spec) InstallArgv(o InstallOptions) ([]string, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	argv := []string{s.Deno, "install", "--frozen=false", "--node-modules-dir=none"}
	if o.Frozen {
		argv[2] = "--frozen"
	}
	if len(o.AllowImport) > 0 {
		argv = append(argv, "--allow-import="+strings.Join(o.AllowImport, ","))
	}
	if s.Entry.Config != "" {
		argv = append(argv, "--config", s.Entry.Config)
	}
	argv = append(argv, "--entrypoint", s.Entry.Path)
	return append(argv, s.Entry.Preload...), nil
}

func (s Spec) Env(base []string) ([]string, error) {
	dir, err := s.DenoDir()
	if err != nil {
		return nil, err
	}
	env := slices.DeleteFunc(slices.Clone(base), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "DENO_DIR" || k == "DENO_NO_UPDATE_CHECK" || k == "NO_COLOR"
	})
	env = append(env, "DENO_DIR="+dir, "NO_COLOR=1")
	if s.Mode == Production {
		env = append(env, "DENO_NO_UPDATE_CHECK=1")
	}
	return env, nil
}

func (s Spec) Command(ctx context.Context, base []string) (*exec.Cmd, error) {
	argv, err := s.Argv()
	if err != nil {
		return nil, err
	}
	return s.command(ctx, argv, base)
}

func (s Spec) command(ctx context.Context, argv, base []string) (*exec.Cmd, error) {
	env, err := s.Env(base)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = s.Dir, env
	return cmd, nil
}

func (s Spec) Install(ctx context.Context, base []string, o InstallOptions) ([]byte, error) {
	argv, err := s.InstallArgv(o)
	if err != nil {
		return nil, err
	}
	dir, _ := s.DenoDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	cmd, err := s.command(ctx, argv, base)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("denorun: %s: %w: %s", strings.Join(argv[1:], " "), err, lastLines(out.String(), 6))
	}
	return out.Bytes(), nil
}

func lastLines(s string, n int) string {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func Collect(root string, keep []string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() || !keyName.MatchString(e.Name()) || slices.Contains(keep, e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(p, 0o700)
			}
			return nil
		})
		if err := os.RemoveAll(dir); err != nil {
			return removed, err
		}
		removed = append(removed, e.Name())
	}
	sort.Strings(removed)
	return removed, nil
}
