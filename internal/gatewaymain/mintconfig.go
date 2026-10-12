package gatewaymain

import (
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
)

func mintCredentials(dir string) func(did, cluster string) (*rest.Config, error) {
	return func(did, cluster string) (*rest.Config, error) {
		if did == "" || cluster == "" {
			return nil, fmt.Errorf("a mint credential is held for one cluster of one account, and "+
				"this lookup names account %q and cluster %q", did, cluster)
		}
		key, err := kcpclient.ClusterKey(kcpclient.HomePath(did, cluster))
		if err != nil {
			return nil, err
		}
		if errs := validation.IsDNS1123Subdomain(key); len(errs) > 0 {
			return nil, fmt.Errorf("cluster %q of %q has key %q, which is not a DNS-1123 subdomain: %s",
				cluster, did, key, errs[0])
		}

		path := filepath.Join(dir, key)
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("no mint credential for cluster %q of %q (%s): %w",
				cluster, did, key, err)
		}
		if !carriesACredential(cfg) {
			return nil, fmt.Errorf(
				"the mint kubeconfig for cluster %q of %q (%s) carries no credential: it would send "+
					"anonymous and fail as an authentication error", cluster, did, path)
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
