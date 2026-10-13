package gatewaykcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
)

const MemberKubeconfig = "members.kubeconfig"

func memberRestConfig(path string) (*rest.Config, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, err
	}
	cfg.Host, _, _ = strings.Cut(strings.TrimSuffix(cfg.Host, "/"), "/clusters/")
	return kcpserver.FollowTokenFile(cfg), nil
}

const (
	MemberToken    = kcpserver.MemberTokenFile
	kcpAdminUser   = kcpserver.KcpAdminUser
	memberTokenGap = 5 * time.Second
)

func followMemberToken(ctx context.Context, slotRoot func() string, tokenFile string, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(memberTokenGap):
		}
		changed, err := kcpserver.RefreshMemberToken(slotRoot(), tokenFile)
		if err != nil {
			log.Warn("the members' kcp-admin token could not be read from the active slot", "err", err.Error())
		} else if changed {
			log.Warn("the members' kcp-admin token lagged the active kcp slot; the fallback check rewrote it, not the roll at its flip", "slotRoot", slotRoot(), "tokenFile", tokenFile)
		}
	}
}

func writeMemberKubeconfig(slotRoot, frontURL, out string) error {
	cfg, err := clientcmd.LoadFromFile(filepath.Join(slotRoot, "admin.kubeconfig"))
	if err != nil {
		return err
	}
	tokenFile := filepath.Join(filepath.Dir(out), MemberToken)
	if _, err := kcpserver.RefreshMemberToken(slotRoot, tokenFile); err != nil {
		return err
	}
	if u, ok := cfg.AuthInfos[kcpAdminUser]; ok {
		u.Token, u.TokenFile = "", tokenFile
	}
	front, err := url.Parse(frontURL)
	if err != nil {
		return err
	}
	for name, c := range cfg.Clusters {
		u, err := url.Parse(c.Server)
		if err != nil {
			return fmt.Errorf("gatewaykcp: cluster %s server %q: %w", name, c.Server, err)
		}
		u.Scheme, u.Host = front.Scheme, front.Host
		c.Server = u.String()
		c.TLSServerName = "127.0.0.1"
	}
	tmp := out + ".tmp"
	if err := clientcmd.WriteToFile(*cfg, tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
