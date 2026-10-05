package main

import (
	"strings"
	"testing"
	"time"
)

func envFor(extra map[string]string) func(string) string {
	env := map[string]string{
		"SOCIALWEB_TENANTCREDENTIAL_STATE_DIR":     "/var/lib/socialweb/clusterprovider",
		"SOCIALWEB_TENANTCREDENTIAL_CLUSTER":       "tenant-a",
		"SOCIALWEB_TENANTCREDENTIAL_SERVER":        "https://tenant-a.localhost:6443",
		"SOCIALWEB_TENANTCREDENTIAL_COMMON_NAME":   "system:admin",
		"SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS": "system:masters",
		"SOCIALWEB_TENANTCREDENTIAL_OUT_DIR":       "/tmp/tenant-a-cred",
	}
	for k, v := range extra {
		if v == "" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

func TestACompleteConfigurationLoads(t *testing.T) {
	cfg, err := load(envFor(nil))
	if err != nil {
		t.Fatalf("a fully configured load was refused: %v", err)
	}
	if cfg.StateDir != "/var/lib/socialweb/clusterprovider" || cfg.Cluster != "tenant-a" ||
		cfg.Server != "https://tenant-a.localhost:6443" || cfg.CommonName != "system:admin" ||
		cfg.OutDir != "/tmp/tenant-a-cred" {
		t.Fatalf("the configuration was not carried into the Config: %+v", cfg)
	}
	if len(cfg.Organizations) != 1 || cfg.Organizations[0] != "system:masters" {
		t.Fatalf("organizations = %v, want [system:masters]", cfg.Organizations)
	}
	if cfg.ValidFor != 0 {
		t.Fatalf("an unset ValidFor = %s, want the zero duration so the minting path's own default "+
			"applies rather than one this tool invented", cfg.ValidFor)
	}
}

func TestEveryRequiredSettingIsNamedWhenMissing(t *testing.T) {
	for _, name := range []string{
		"SOCIALWEB_TENANTCREDENTIAL_STATE_DIR",
		"SOCIALWEB_TENANTCREDENTIAL_CLUSTER",
		"SOCIALWEB_TENANTCREDENTIAL_SERVER",
		"SOCIALWEB_TENANTCREDENTIAL_COMMON_NAME",
		"SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS",
		"SOCIALWEB_TENANTCREDENTIAL_OUT_DIR",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(envFor(map[string]string{name: ""}))
			if err == nil {
				t.Fatalf("%s was unset and the load was accepted: there is no default for it, and "+
					"a run that proceeded would mint or write something the operator did not ask for", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("the refusal does not name %s, so an operator cannot tell which setting it "+
					"is about: %v", name, err)
			}
		})
	}
}

func TestNoOrganizationsIsRefusedAndNotDefaulted(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		_, err := load(envFor(map[string]string{"SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS": raw}))
		if err == nil {
			t.Fatalf("%q was accepted as the organizations. A certificate with no organization "+
				"carries no group, so the apiserver sees an unprivileged user and every object the "+
				"tenant grant exists to apply is refused", raw)
		}
		if !strings.Contains(err.Error(), "SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS") {
			t.Fatalf("%q: the refusal does not name the variable an operator set: %v", raw, err)
		}
	}
}

func TestOrganizationsAreSplitAndTrimmed(t *testing.T) {
	cfg, err := load(envFor(map[string]string{
		"SOCIALWEB_TENANTCREDENTIAL_ORGANIZATIONS": " system:masters , gitlab-runner ",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"system:masters", "gitlab-runner"}
	if len(cfg.Organizations) != len(want) {
		t.Fatalf("organizations = %v, want %v", cfg.Organizations, want)
	}
	for i := range want {
		if cfg.Organizations[i] != want[i] {
			t.Fatalf("organizations = %v, want %v -- a value a runner's RBAC binds must survive "+
				"parsing exactly, because a stray space is a group no rule matches", cfg.Organizations, want)
		}
	}
}

func TestValidForIsOptionalAndAMalformedOneIsRefused(t *testing.T) {
	cfg, err := load(envFor(map[string]string{"SOCIALWEB_TENANTCREDENTIAL_VALID_FOR": "720h"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ValidFor != 720*time.Hour {
		t.Fatalf("ValidFor = %s, want 720h", cfg.ValidFor)
	}

	for _, raw := range []string{"forever", "-1h", "0s", "0"} {
		_, err := load(envFor(map[string]string{"SOCIALWEB_TENANTCREDENTIAL_VALID_FOR": raw}))
		if err == nil {
			t.Fatalf("%q was accepted as a validity. A malformed or non-positive one must fail here "+
				"rather than be replaced by the minting path's default, which reads as the requested "+
				"duration being in force when it is not", raw)
		}
		if !strings.Contains(err.Error(), "SOCIALWEB_TENANTCREDENTIAL_VALID_FOR") {
			t.Fatalf("%q: the refusal does not name the variable an operator set: %v", raw, err)
		}
	}
}
