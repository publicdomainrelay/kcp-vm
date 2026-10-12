package gatewaymain

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

func completeEnv() map[string]string {
	return map[string]string{
		envAddr:            "127.0.0.1:8443",
		envAudiences:       "did:web:gateway.example, did:web:gateway.example#socialweb",
		envKCPHost:         "https://kcp.example.com:6443",
		envKCPKubeconfig:   "/etc/socialweb/gateway.kubeconfig",
		envPLCURL:          "https://plc.directory",
		envMintKubeconfigs: "/etc/socialweb/mint",
		envDevkeyRegistry:  "root:devkey-registry",

		envDeviceKeyAudience:    "did:web:gateway.example",
		envDeviceKeyTTL:         "1h",
		envDeviceKeyExecCommand: "/usr/local/bin/socialweb-credential",
		envDeviceKeyExecEnvName: "SOCIALWEB_DEVICE_CREDENTIAL",
	}
}

func TestLoadRefusesAMissingRequiredVariable(t *testing.T) {
	for _, name := range []string{
		envAddr, envAudiences, envKCPHost, envKCPKubeconfig, envPLCURL,
		envMintKubeconfigs, envDevkeyRegistry,
		envDeviceKeyAudience, envDeviceKeyTTL, envDeviceKeyExecCommand, envDeviceKeyExecEnvName,
	} {
		t.Run(name, func(t *testing.T) {
			pairs := completeEnv()
			delete(pairs, name)
			if _, err := load(env(pairs)); err == nil {
				t.Fatalf("%s unset was accepted; a gateway with no %s must not start", name, name)
			} else if !strings.Contains(err.Error(), name) {
				t.Fatalf("the refusal does not name the variable: %v", err)
			}
		})
	}

	pairs := completeEnv()
	pairs[envAudiences] = " , ,"
	if _, err := load(env(pairs)); err == nil {
		t.Fatal("an audience list that is set but empty was accepted")
	}
}

func TestLoadSplitsTheAudienceListAndTrimsIt(t *testing.T) {
	pairs := completeEnv()
	pairs[envAudiences] = " did:web:a.example , did:web:a.example#svc ,, "
	cfg, err := load(env(pairs))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"did:web:a.example", "did:web:a.example#svc"}
	if len(cfg.Audiences) != len(want) {
		t.Fatalf("audiences = %q, want %q", cfg.Audiences, want)
	}
	for i := range want {
		if cfg.Audiences[i] != want[i] {
			t.Fatalf("audiences = %q, want %q", cfg.Audiences, want)
		}
	}
}

func TestLoadRefusesAValueThatDoesNotParse(t *testing.T) {
	cases := []struct {
		name string
		env  string
		raw  string
	}{
		{"a duration that is not one", envMintTTL, "banana"},
		{"a zero mint ttl", envMintTTL, "0s"},
		{"a device key ttl that is not a duration", envDeviceKeyTTL, "banana"},
		{"a device key ttl of zero, which internal/devicekey refuses as a lifetime nobody chose", envDeviceKeyTTL, "0s"},
		{"a negative device key ttl", envDeviceKeyTTL, "-1h"},
		{"a negative failure hold", envMintFailureHold, "-1s"},
		{"a directory ttl of zero, which the cache reads as UNLIMITED", envDirectoryTTL, "0"},
		{"a negative shutdown timeout", envShutdownTimeout, "-5s"},
		{"a log level that is not one", envLogLevel, "loud"},
		{"a device key max ttl that is not a duration", envDeviceKeyMaxTTL, "banana"},
		{"a device key max ttl of zero", envDeviceKeyMaxTTL, "0s"},
		{"a public kcp host that is plain http", envKCPPublicHost, "http://kcp.example.com"},
		{"a public kcp host with no host at all", envKCPPublicHost, "https://"},
		{"a public mint url that is not a URL", envPublicMintURL, "://socialweb.example"},
		{"a public mint url that is not https", envPublicMintURL, "http://socialweb.example/v1/credential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pairs := completeEnv()
			pairs[tc.env] = tc.raw
			if _, err := load(env(pairs)); err == nil {
				t.Fatalf("%s=%q was accepted; a server must not start misconfigured", tc.env, tc.raw)
			}
		})
	}
}

func TestLoadDefaultsTheDeviceKeyCeilingAndKeepsThePublicFrontOptional(t *testing.T) {
	cfg, err := load(env(completeEnv()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DeviceKeyMaxTTL != cfg.DeviceKeyTTL {
		t.Fatalf("with %s unset the ceiling is %s and the lifetime is %s: unset means the "+
			"ceiling IS the deployment's own policy, so no caller can ask for more",
			envDeviceKeyMaxTTL, cfg.DeviceKeyMaxTTL, cfg.DeviceKeyTTL)
	}
	if cfg.KCPPublicHost != "" || cfg.PublicMintURL != "" {
		t.Fatalf("unset public-front settings resolved to %q/%q, want empty: with them empty "+
			"the rendered kubeconfig keeps naming the loopback kcp this gateway speaks to",
			cfg.KCPPublicHost, cfg.PublicMintURL)
	}

	pairs := completeEnv()
	pairs[envDeviceKeyMaxTTL] = "2h"
	pairs[envKCPPublicHost] = "https://kcp.socialweb.example/"
	pairs[envPublicMintURL] = "https://socialweb.example/v1/credential"
	if cfg, err = load(env(pairs)); err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DeviceKeyMaxTTL != 2*time.Hour {
		t.Fatalf("device key max ttl = %s, want 2h", cfg.DeviceKeyMaxTTL)
	}
	if cfg.KCPPublicHost != "https://kcp.socialweb.example" {
		t.Fatalf("public kcp host = %q, want the trailing slash trimmed", cfg.KCPPublicHost)
	}
	if cfg.PublicMintURL != "https://socialweb.example/v1/credential" {
		t.Fatalf("public mint url = %q", cfg.PublicMintURL)
	}
}

func TestLoadRefusesANonLoopbackAddr(t *testing.T) {
	for _, raw := range []string{"0.0.0.0:8080", ":8080", "192.0.2.10:8080"} {
		t.Run("refuses "+raw, func(t *testing.T) {
			pairs := completeEnv()
			pairs[envAddr] = raw
			_, err := load(env(pairs))
			if err == nil {
				t.Fatalf("%s=%q was accepted; bearer service-auth tokens arrive and device "+
					"credentials leave through this listener, so it must bind loopback only", envAddr, raw)
			}
			if !strings.Contains(err.Error(), envAddr) {
				t.Fatalf("the refusal does not name %s: %v", envAddr, err)
			}
		})
	}
	for _, raw := range []string{"127.0.0.1:8080", "[::1]:8080"} {
		t.Run("accepts "+raw, func(t *testing.T) {
			pairs := completeEnv()
			pairs[envAddr] = raw
			if _, err := load(env(pairs)); err != nil {
				t.Fatalf("%s=%q was refused, but it is a loopback address: %v", envAddr, raw, err)
			}
		})
	}
}

func TestLoadDefaultsAreTheOnceThatDoNotHaveToBeChosen(t *testing.T) {
	cfg, err := load(env(completeEnv()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DirectoryCacheTTL != DefaultDirectoryCacheTTL {
		t.Fatalf("directory cache ttl = %s, want %s (ADR 0020's revalidation window)",
			cfg.DirectoryCacheTTL, DefaultDirectoryCacheTTL)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("shutdown timeout = %s, want %s", cfg.ShutdownTimeout, DefaultShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("log level = %s, want info", cfg.LogLevel)
	}
	if cfg.MintTTL != 0 || cfg.MintFailureHold != 0 {
		t.Fatalf("mint policy = %s/%s, want zero so internal/mint's defaults apply",
			cfg.MintTTL, cfg.MintFailureHold)
	}

	pairs := completeEnv()
	pairs[envMintTTL] = "600s"
	pairs[envDirectoryTTL] = "5s"
	if cfg, err = load(env(pairs)); err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MintTTL != 600*time.Second {
		t.Fatalf("mint ttl = %s, want 600s (ADR 0020's floor)", cfg.MintTTL)
	}
	if cfg.DirectoryCacheTTL != 5*time.Second {
		t.Fatalf("directory cache ttl = %s, want 5s", cfg.DirectoryCacheTTL)
	}
}
