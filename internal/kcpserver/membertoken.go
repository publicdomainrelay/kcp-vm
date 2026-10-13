package kcpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
