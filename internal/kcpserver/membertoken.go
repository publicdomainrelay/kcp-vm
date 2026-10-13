package kcpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	MemberTokenFile = "members.token"
	KcpAdminUser    = "kcp-admin"
)

func MemberTokenPath(dir string) string { return filepath.Join(dir, MemberTokenFile) }

func SlotAdminToken(slotRoot string) (string, error) {
	cfg, err := clientcmd.LoadFromFile(filepath.Join(slotRoot, "admin.kubeconfig"))
	if err != nil {
		return "", err
	}
	u, ok := cfg.AuthInfos[KcpAdminUser]
	if !ok || u.Token == "" {
		return "", fmt.Errorf("kcpserver: %s/admin.kubeconfig holds no %s token", slotRoot, KcpAdminUser)
	}
	return u.Token, nil
}

func RefreshMemberToken(slotRoot, tokenFile string) (bool, error) {
	token, err := SlotAdminToken(slotRoot)
	if err != nil {
		return false, err
	}
	if old, err := os.ReadFile(tokenFile); err == nil && strings.TrimSpace(string(old)) == token {
		return false, nil
	}
	return true, writeTokenAtomic(tokenFile, []byte(token+"\n"))
}

func RefreshMemberTokenIfPresent(slotRoot, tokenFile string) (bool, error) {
	if _, err := os.Stat(tokenFile); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return RefreshMemberToken(slotRoot, tokenFile)
}

func writeTokenAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

type tokenFileTransport struct {
	path  string
	base  http.RoundTripper
	mu    sync.Mutex
	stamp time.Time
	size  int64
	token string
}

func (t *tokenFileTransport) current() string {
	st, err := os.Stat(t.path)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		return t.token
	}
	if t.token != "" && st.ModTime().Equal(t.stamp) && st.Size() == t.size {
		return t.token
	}
	b, err := os.ReadFile(t.path)
	if err != nil {
		return t.token
	}
	t.token, t.stamp, t.size = strings.TrimSpace(string(b)), st.ModTime(), st.Size()
	return t.token
}

func (t *tokenFileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	token := t.current()
	if token == "" {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req)
}

func FollowTokenFile(cfg *rest.Config) *rest.Config {
	if cfg == nil || cfg.BearerTokenFile == "" {
		return cfg
	}
	path := cfg.BearerTokenFile
	cfg.BearerToken, cfg.BearerTokenFile = "", ""
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper { return &tokenFileTransport{path: path, base: rt} })
	return cfg
}
