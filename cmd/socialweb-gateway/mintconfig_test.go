package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMintCredentialsFailsClosedOnAnUnknownCluster(t *testing.T) {
	configs := mintCredentials(writeMintKubeconfigs(t, "live"))

	cfg, err := configs("live")
	if err != nil {
		t.Fatalf("the configured cluster was refused: %v", err)
	}
	if cfg == nil {
		t.Fatal("the configured cluster returned a nil config with no error")
	}

	cfg, err = configs("other")
	if err == nil {
		t.Fatal("an UNKNOWN cluster returned a credential. This is the failure the interface " +
			"exists to prevent: the mint would proceed as some other cluster's identity, and " +
			"nothing on the wire would say so")
	}
	if cfg != nil {
		t.Fatalf("an unknown cluster returned an error AND a usable config (%v), so a caller "+
			"that ignored the error would still mint as somebody else", cfg)
	}
	if !strings.Contains(err.Error(), "other") {
		t.Fatalf("the refusal does not name the cluster: %v", err)
	}
}

func TestMintCredentialsRefusesALabelThatCouldClimbOutOfTheDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "outside.kubeconfig")
	if err := os.WriteFile(outside, []byte(mintKubeconfigDoc), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	configs := mintCredentials(dir)

	for _, label := range []string{
		"",
		"..",
		"../outside.kubeconfig",
		"live/../../outside",
		"/etc/passwd",
		"live.kubeconfig",
		strings.Repeat("a", 64),
		"LIVE",
	} {
		t.Run(label, func(t *testing.T) {
			cfg, err := configs(label)
			if err == nil {
				t.Fatalf("the label %q was accepted and returned a config: a cluster label is "+
					"joined onto the credential directory, so this is a read outside it", label)
			}
			if cfg != nil {
				t.Fatalf("the label %q returned a usable config alongside its error", label)
			}
		})
	}
}

func TestMintCredentialsRefusesAKubeconfigWithNoCredentialInIt(t *testing.T) {
	dir := t.TempDir()
	const anonymous = `apiVersion: v1
kind: Config
clusters:
- name: tenant
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: tenant
  context:
    cluster: tenant
current-context: tenant
`
	if err := os.WriteFile(filepath.Join(dir, "live"), []byte(anonymous), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	cfg, err := mintCredentials(dir)("live")
	if err == nil {
		t.Fatalf("a kubeconfig with no user was accepted (%v); the mint would send it "+
			"anonymously and come back 401", cfg)
	}
	if !strings.Contains(err.Error(), "no credential") {
		t.Fatalf("the refusal does not say what is wrong with the kubeconfig: %v", err)
	}
}

const mintKubeconfigDoc = `apiVersion: v1
kind: Config
clusters:
- name: tenant
  cluster:
    server: https://127.0.0.1:6443
users:
- name: gwmint
  user:
    token: test-only-not-a-credential
contexts:
- name: tenant
  context:
    cluster: tenant
    user: gwmint
current-context: tenant
`
