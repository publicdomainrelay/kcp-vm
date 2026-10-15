package providermain

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/instance"
)

type tokenStore struct {
	stateDir string
	log      *slog.Logger

	mu sync.Mutex
	by map[string]instance.Token
}

func newTokenStore(stateDir string, log *slog.Logger) *tokenStore {
	return &tokenStore{stateDir: stateDir, log: log, by: map[string]instance.Token{}}
}

func (s *tokenStore) token(cluster string) instance.Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.by[cluster]; ok {
		return t
	}
	t, err := instance.New(nil)
	if err != nil {
		s.log.Error("cluster provider: minting the instance token failed", "cluster", cluster, "error", err.Error())
		return ""
	}
	path, err := s.path(cluster)
	if err != nil {
		s.log.Error("cluster provider: no token path", "cluster", cluster, "error", err.Error())
		return ""
	}
	if err := instance.WriteFile(path, t); err != nil {
		s.log.Error("cluster provider: writing the instance token failed",
			"cluster", cluster, "path", path, "error", err.Error())
		return ""
	}
	s.by[cluster] = t
	s.log.Info("cluster provider: instance token minted and written", "cluster", cluster, "path", path)
	return t
}

func (s *tokenStore) forget(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.by, cluster)
}

func (s *tokenStore) path(cluster string) (string, error) {
	dir, err := clusterprovider.ClusterDataDir(s.stateDir, cluster)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return clusterprovider.TokenPath(filepath.Clean(dir)), nil
}
