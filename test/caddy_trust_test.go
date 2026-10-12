package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	rendersCaddy  = regexp.MustCompile(`(?m)(\.caddy\b|/Caddyfile\b|"apps"\s*:\s*\{[^}]*"http")`)
	pinsNoTrust   = regexp.MustCompile(`(skip_install_trust|"install_trust"\s*:\s*false)`)
	internalIssue = regexp.MustCompile(`(?m)(^\s*tls\s+internal\b|"(issuer|module)"\s*:\s*"internal")`)
)

func TestEveryCaddyConfigInTheRepoPinsInstallTrustFalse(t *testing.T) {
	seen := 0
	for _, root := range []string{"../deploy", "../scripts"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			name := d.Name()
			isCaddy := strings.HasPrefix(name, "Caddyfile") || strings.HasSuffix(name, ".caddy") || rendersCaddy.Match(b)
			if !isCaddy {
				return nil
			}
			seen++
			if !pinsNoTrust.Match(b) {
				t.Errorf("%s renders or ships caddy config without pinning skip_install_trust (Caddyfile) or install_trust false (JSON): caddy's local CA must never be installed into the system trust store (kcp-blue-green.md section 6)", path)
			}
			if internalIssue.Match(b) {
				t.Errorf("%s uses caddy's internal issuer", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen == 0 {
		t.Fatal("no caddy config found under deploy/ or scripts/: the scan is looking in the wrong place")
	}
}
