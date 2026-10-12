package gatewaykcp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const MemberKubeconfig = "members.kubeconfig"

func memberRestConfig(path string) (*rest.Config, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, err
	}
	cfg.Host, _, _ = strings.Cut(strings.TrimSuffix(cfg.Host, "/"), "/clusters/")
	return cfg, nil
}

func writeMemberKubeconfig(sharedDir, frontURL, out string) error {
	cfg, err := clientcmd.LoadFromFile(filepath.Join(sharedDir, "admin.kubeconfig"))
	if err != nil {
		return err
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
