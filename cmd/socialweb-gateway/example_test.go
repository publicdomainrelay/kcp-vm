package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const envExamplePath = "../../deploy/socialweb-gateway.env.example"

var unitSuppliedEnv = []string{"SOCIALWEB_KCP_KUBECONFIG"}

func TestTheEnvExampleLoads(t *testing.T) {
	raw, err := os.ReadFile(envExamplePath)
	if err != nil {
		t.Fatalf("reading %s: %v", envExamplePath, err)
	}

	pairs := parseEnvExample(t, string(raw))
	for _, name := range unitSuppliedEnv {
		if _, ok := pairs[name]; ok {
			t.Fatalf("%s sets %s, which the systemd unit supplies as a LoadCredential; settings "+
				"from an EnvironmentFile override the unit's Environment=, so a path set here "+
				"would replace the credential file the unit hands the process at run time",
				envExamplePath, name)
		}
	}

	lookup := func(name string) string {
		if v, ok := pairs[name]; ok {
			return v
		}
		for _, n := range unitSuppliedEnv {
			if n == name {
				return "/run/credentials/socialweb-gateway.service/kcp.kubeconfig"
			}
		}
		return ""
	}
	if _, err := load(lookup); err != nil {
		t.Fatalf("%s does not load: %v", envExamplePath, err)
	}

	declared := declaredEnvConstants(t, "config.go")
	if len(declared) == 0 {
		t.Fatal("no env constants were found in config.go, so the completeness check would " +
			"pass vacuously")
	}
	for _, name := range declared {
		if slices.Contains(unitSuppliedEnv, name) {
			continue
		}
		if _, ok := pairs[name]; !ok {
			t.Fatalf("%s does not set %s, which config.go declares; a deployment built from "+
				"this example would start with %s unset", envExamplePath, name, name)
		}
	}
}

func parseEnvExample(t *testing.T, raw string) map[string]string {
	t.Helper()
	pairs := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("the example line %q is not NAME=VALUE", line)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			t.Fatalf("the example line %q has no name", line)
		}
		pairs[name] = strings.TrimSpace(value)
	}
	return pairs
}

var envConstPattern = regexp.MustCompile(`(?m)^\s*env\w+\s*=\s*"([^"]+)"`)

func declaredEnvConstants(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var names []string
	for _, match := range envConstPattern.FindAllStringSubmatch(string(raw), -1) {
		names = append(names, match[1])
	}
	return names
}
