package vaultlayout

import (
	"testing"
	"time"
)

func findRole(l Layout, vault VaultName, name string) int {
	for i, r := range l.AppRoles {
		if r.Vault == vault && r.Name == name {
			return i
		}
	}
	return -1
}

func findSecret(l Layout, vault VaultName, class SecretClassName) int {
	for i, s := range l.Secrets {
		if s.Vault == vault && s.Class == class {
			return i
		}
	}
	return -1
}

func clone(l Layout) Layout {
	l.Mounts = append([]Mount(nil), l.Mounts...)
	l.Policies = append([]Policy(nil), l.Policies...)
	l.AppRoles = append([]AppRole(nil), l.AppRoles...)
	l.PKIRoles = append([]PKIRole(nil), l.PKIRoles...)
	l.Secrets = append([]Secret(nil), l.Secrets...)
	l.SecretClasses = append([]SecretClass(nil), l.SecretClasses...)
	return l
}

func TestDefaultLayoutIsConsistent(t *testing.T) {
	if err := Validate(Default()); err != nil {
		t.Fatalf("the declared layout is inconsistent: %v", err)
	}
}

func TestValidatorRejectsInconsistentLayouts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(l *Layout)
	}{
		{
			name: "policy references a path no mount provides",
			mutate: func(l *Layout) {
				l.Policies = append(l.Policies, Policy{
					Vault: Root,
					Name:  "ghost-policy",
					HCL:   readPolicy("nowhere/data/socialweb/ghost"),
				})
			},
		},
		{
			name: "consumer has no role",
			mutate: func(l *Layout) {
				i := findSecret(*l, Root, ClassKCPClientCA)
				l.Secrets[i].Consumer = "ghost"
			},
		},
		{
			name: "role references a missing policy",
			mutate: func(l *Layout) {
				i := findRole(*l, Root, RoleGateway)
				l.AppRoles[i].Policies = append([]string{"ghost-policy"}, l.AppRoles[i].Policies...)
			},
		},
		{
			name: "PKI role max TTL exceeds the mount",
			mutate: func(l *Layout) {
				l.PKIRoles[0].MaxTTL = 10000 * time.Hour
			},
		},
		{
			name: "secret class assigned to neither vault",
			mutate: func(l *Layout) {
				i := findSecret(*l, Cluster, ClassPerClusterSSHKey)
				l.Secrets = append(l.Secrets[:i], l.Secrets[i+1:]...)
			},
		},
		{
			name: "secret class assigned to both vaults",
			mutate: func(l *Layout) {
				l.Secrets = append(l.Secrets, Secret{
					Vault:    Root,
					Path:     PathClusterSSH,
					Class:    ClassPerClusterSSHKey,
					Consumer: RoleClusterNode,
					Keys:     []string{"id_ed25519", "id_ed25519.pub"},
				})
			},
		},
		{
			name: "identity bundle has the wrong key count",
			mutate: func(l *Layout) {
				i := findSecret(*l, Cluster, ClassIdentityBundle)
				l.Secrets[i].Keys = []string{"tls/server-ca.crt"}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := clone(Default())
			tc.mutate(&l)
			if err := Validate(l); err == nil {
				t.Fatalf("validator accepted an inconsistent layout: %s", tc.name)
			}
		})
	}
}

func TestTheDoorKeepsEachClaimsSecretsIngressKeyAtOneKVPath(t *testing.T) {
	got := DoorClaimSecretsIngressPath("abc-node3")
	if got != "secret/data/socialweb-door/claims/abc-node3/secrets-ingress" {
		t.Fatalf("the door's per-claim secrets-ingress key lives at %q; the door (TS) and the bao sync "+
			"desired set name this path literally", got)
	}
}
