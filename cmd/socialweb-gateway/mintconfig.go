package main

import (
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func mintCredentials(dir string) func(cluster string) (*rest.Config, error) {
	return func(cluster string) (*rest.Config, error) {
		if errs := validation.IsDNS1123Label(cluster); len(errs) > 0 {
			return nil, fmt.Errorf("cluster %q is not a DNS-1123 label: %s", cluster, errs[0])
		}

		path := filepath.Join(dir, cluster)
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("no mint credential for cluster %q: %w", cluster, err)
		}
		if !carriesACredential(cfg) {
			return nil, fmt.Errorf(
				"the mint kubeconfig for cluster %q (%s) carries no credential: it would send "+
					"anonymous and fail as an authentication error", cluster, path)
		}
		return cfg, nil
	}
}

func carriesACredential(cfg *rest.Config) bool {
	return cfg.BearerToken != "" || cfg.BearerTokenFile != "" ||
		len(cfg.CertData) > 0 || cfg.CertFile != "" ||
		len(cfg.KeyData) > 0 || cfg.KeyFile != "" ||
		cfg.Username != "" || cfg.Password != "" ||
		cfg.AuthProvider != nil || cfg.ExecProvider != nil
}
