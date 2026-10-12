package providermain

import (
	"fmt"
	"os/exec"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
)

func (c Config) pinnedExecutables() map[string]string {
	return map[string]string{envK3s: c.K3s, envKine: c.Kine, envBao: c.Bao, envPgDump: c.PgDump}
}

func (c Config) lookPath(name string) (string, error) {
	byName := map[string]struct{ env, path string }{"k3s": {envK3s, c.K3s}, "kine": {envKine, c.Kine},
		"bao": {envBao, c.Bao}, "pg_dump": {envPgDump, c.PgDump}}
	n, pinned := byName[name]
	if pinned && n.path != "" {
		return n.path, nil
	}
	if assets.Release() {
		if pinned {
			return "", fmt.Errorf("a release build runs %s only from its own assets, and %s names none", name, n.env)
		}
		return "", fmt.Errorf("a release build runs %s only from its own assets and never looks in PATH", name)
	}
	return exec.LookPath(name)
}
