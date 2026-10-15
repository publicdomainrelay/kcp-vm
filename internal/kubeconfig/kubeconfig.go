package kubeconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"sigs.k8s.io/yaml"
)

type Input struct {
	ClusterName string

	Server string

	CACertPEM string

	ExecCommand string

	ExecArgs []string

	DeviceCredential string

	DeviceKeyEnvName string

	MintURL string
}

var ErrNotHTTPS = errors.New("kubeconfig: server must be https")

var ErrNoExec = errors.New("kubeconfig: an exec credential helper is required")

const ExecAPIVersion = "client.authentication.k8s.io/v1"

const MintURLEnvName = "SOCIALWEB_MINT_URL"

func Build(in Input) (string, error) {
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
	if in.ExecCommand == "" {
		return "", ErrNoExec
	}
	if in.DeviceKeyEnvName == "" {
		return "", errors.New("kubeconfig: the device key needs an environment variable name")
	}

	cluster := map[string]any{"server": in.Server}
	if in.CACertPEM != "" {
		cluster["certificate-authority-data"] = in.CACertPEM
	}

	env := []map[string]any{
		{"name": in.DeviceKeyEnvName, "value": in.DeviceCredential},
	}
	if in.MintURL != "" {
		env = append(env, map[string]any{"name": MintURLEnvName, "value": in.MintURL})
	}

	user := map[string]any{
		"exec": map[string]any{
			"apiVersion": ExecAPIVersion,
			"command":    in.ExecCommand,
			"args":       in.ExecArgs,

			"interactiveMode": "Never",

			"env": env,
		},
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

var Forbidden = []string{
	"bearer_token",
	"token:",
	"client-certificate-data",
	"client-key-data",
	"admin.kubeconfig",
}

func AssertClean(rendered string) error {
	lower := strings.ToLower(rendered)
	for _, f := range Forbidden {
		if strings.Contains(lower, strings.ToLower(f)) {
			return fmt.Errorf("kubeconfig: rendered document contains %q, which must never "+
				"reach a user -- see ADR 0029's must-not list", f)
		}
	}
	return nil
}

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
