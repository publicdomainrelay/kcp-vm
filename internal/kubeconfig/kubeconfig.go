package kubeconfig

import (
	"errors"
	"fmt"
	"net/url"

	"sigs.k8s.io/yaml"
)

var ErrNotHTTPS = errors.New("kubeconfig: server must be https")

type ClientCertInput struct {
	ClusterName string

	Server string

	CACertPEM []byte

	CertPEM []byte

	KeyPEM []byte
}

func BuildClientCert(in ClientCertInput) (string, error) {
	if in.ClusterName == "" {
		return "", errors.New("kubeconfig: empty cluster name")
	}
	if in.Server == "" {
		return "", errors.New("kubeconfig: empty server")
	}
	u, err := url.Parse(in.Server)
	if err != nil {
		return "", fmt.Errorf("kubeconfig: unparseable server: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("%w (got %q)", ErrNotHTTPS, u.Scheme)
	}
	if len(in.CertPEM) == 0 || len(in.KeyPEM) == 0 {
		return "", errors.New("kubeconfig: a client certificate and its key are required")
	}

	cluster := map[string]any{"server": in.Server}
	if len(in.CACertPEM) > 0 {
		cluster["certificate-authority-data"] = in.CACertPEM
	}
	user := map[string]any{
		"client-certificate-data": in.CertPEM,
		"client-key-data":         in.KeyPEM,
	}
	doc := map[string]any{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []map[string]any{
			{"name": in.ClusterName, "cluster": cluster},
		},
		"users": []map[string]any{
			{"name": in.ClusterName, "user": user},
		},
		"contexts": []map[string]any{
			{"name": in.ClusterName, "context": map[string]any{
				"cluster": in.ClusterName,
				"user":    in.ClusterName,
			}},
		},
		"current-context": in.ClusterName,
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("kubeconfig: marshal: %w", err)
	}
	return string(raw), nil
}
