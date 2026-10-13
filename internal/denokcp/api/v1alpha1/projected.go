package v1alpha1

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const MinProjectedTokenSeconds = int64(600)

var projectedName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func ProjectedEnvName(name string) string {
	return ProjectedEnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func CheckProjected(list []ProjectedCredential) error {
	seen := map[string]bool{}
	for _, c := range list {
		if !projectedName.MatchString(c.Name) {
			return fmt.Errorf("projected %q: the name is not a DNS label", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("projected %q: the name is used twice", c.Name)
		}
		seen[c.Name] = true
		sources := 0
		for _, set := range []bool{c.ServiceAccountToken != nil, c.KcpCA != nil, c.OpenBaoClient != nil, c.File != nil} {
			if set {
				sources++
			}
		}
		if sources != 1 {
			return fmt.Errorf("projected %q: name exactly one of serviceAccountToken, kcpCA, openBaoClient, file", c.Name)
		}
		switch {
		case c.ServiceAccountToken != nil:
			t := c.ServiceAccountToken
			if !projectedName.MatchString(t.Name) {
				return fmt.Errorf("projected %q: serviceAccountToken names no ServiceAccount", c.Name)
			}
			if t.ExpirationSeconds != 0 && t.ExpirationSeconds < MinProjectedTokenSeconds {
				return fmt.Errorf("projected %q: expirationSeconds is below %d", c.Name, MinProjectedTokenSeconds)
			}
		case c.OpenBaoClient != nil:
			if c.OpenBaoClient.Server == "" || c.OpenBaoClient.Client == "" {
				return fmt.Errorf("projected %q: openBaoClient names no server or client", c.Name)
			}
		case c.File != nil:
			if !filepath.IsAbs(c.File.Path) {
				return fmt.Errorf("projected %q: file %q is not an absolute path", c.Name, c.File.Path)
			}
		}
	}
	return nil
}
