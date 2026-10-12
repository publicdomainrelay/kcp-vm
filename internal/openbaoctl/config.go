package openbaoctl

import (
	"fmt"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/pgunit"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/rootvault"
)

func serverOptions(s v1alpha1.OpenBaoServerSpec, clientCABundle string) (rootvault.ServerOptions, error) {
	o := rootvault.ServerOptions{UnauthedGenerateRoot: true, ClientCAFile: clientCABundle}
	if t := s.TLS; t != nil {
		for _, p := range []string{t.CertFile, t.KeyFile, t.CAFile} {
			if !filepath.IsAbs(p) {
				return o, fmt.Errorf("spec.tls names %q; certFile, keyFile and caFile are absolute paths", p)
			}
		}
		o.CertFile, o.KeyFile, o.ClientCertOptional = t.CertFile, t.KeyFile, t.ClientCertOptional
	}
	if s.Storage != nil && s.Storage.Postgres != nil {
		pg := s.Storage.Postgres
		if !filepath.IsAbs(pg.SocketDir) || pg.User == "" || pg.Database == "" {
			return o, fmt.Errorf("spec.storage.postgres needs an absolute socketDir, a user and a database (got %q, %q, %q)",
				pg.SocketDir, pg.User, pg.Database)
		}
		o.PostgresDSN = pgunit.DSN(pg.SocketDir, pg.User, pg.Database)
	}
	return o, nil
}

func caFile(s v1alpha1.OpenBaoServerSpec, dir string) string {
	if s.TLS != nil {
		return s.TLS.CAFile
	}
	return rootvault.TLSCAPath(dir)
}
