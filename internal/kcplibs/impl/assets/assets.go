package assets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type Set struct {
	Dir string

	Files map[string][]byte

	Perm os.FileMode

	mu sync.Mutex

	written bool

	paths map[string]string

	err error
}

var ErrNoDirectory = errors.New("assets: a directory is required")

func (s *Set) Materialise() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.written {
		if err := s.write(); err != nil {
			s.err = err
			return s.published(), err
		}
		s.err = nil
		s.written = true
	}
	return s.published(), s.err
}

func (s *Set) Rewrite() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(); err != nil {
		s.err = err
		s.written = false
		return s.published(), err
	}
	s.err = nil
	s.written = true
	return s.published(), nil
}

func (s *Set) published() map[string]string {
	out := make(map[string]string, len(s.paths))
	for name, path := range s.paths {
		out[name] = path
	}
	return out
}

func (s *Set) Path(name string) (string, error) {
	if s.Dir == "" {
		return "", ErrNoDirectory
	}
	paths, err := s.Materialise()
	if err != nil {
		return "", err
	}
	path, ok := paths[name]
	if !ok {
		return "", fmt.Errorf("assets: %s is not in the set", name)
	}
	return path, nil
}

func (s *Set) write() error {
	if s.Dir == "" {
		return ErrNoDirectory
	}
	perm := s.Perm
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("assets: create %s: %w", s.Dir, err)
	}
	names := make([]string, 0, len(s.Files))
	for name := range s.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	paths := make(map[string]string, len(names))
	for _, name := range names {
		path := filepath.Join(s.Dir, name)
		if dir := filepath.Dir(path); dir != s.Dir {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("assets: create %s: %w", dir, err)
			}
		}
		if err := os.WriteFile(path, s.Files[name], perm); err != nil {
			return fmt.Errorf("assets: write %s: %w", path, err)
		}
		paths[name] = path
	}
	s.paths = paths
	return nil
}
