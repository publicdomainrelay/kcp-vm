package openbaoctl

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultcert"
)

const (
	CertAuthMount = "cert"
	CertAuthRole  = "socialweb-core"
	CorePolicy    = "socialweb-core"
	KVMount       = "secret"
	CoreTokenTTL  = "15m"
	CoreTokenMax  = "1h"

	corePolicyHCL = `path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}
`
)

var (
	clientNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reservedMountNames = map[string]bool{KVMount: true, CertAuthMount: true, "sys": true, "auth": true, "identity": true, "cubbyhole": true}
)

func ClientPolicy(name string) string { return "client-" + name }

func clientPolicyHCL(mount string) string {
	return fmt.Sprintf(`path "%[1]s/data/*" {
  capabilities = ["create", "read", "update", "patch", "delete"]
}
path "%[1]s/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
`, mount)
}

func checkClients(clients []v1alpha1.OpenBaoServerClient) error {
	names, mounts := map[string]bool{}, map[string]bool{}
	for _, c := range clients {
		if !clientNamePattern.MatchString(c.Name) || c.Name == CertAuthRole {
			return fmt.Errorf("client name %q: lower case letters, digits and dashes, and never %s", c.Name, CertAuthRole)
		}
		if !clientNamePattern.MatchString(c.KVMount) || reservedMountNames[c.KVMount] {
			return fmt.Errorf("client %s: kvMount %q is not a mount of its own (never %s, %s or a system mount)", c.Name, c.KVMount, KVMount, CertAuthMount)
		}
		if names[c.Name] || mounts[c.KVMount] {
			return fmt.Errorf("client %s: names and kvMounts are each used once", c.Name)
		}
		names[c.Name], mounts[c.KVMount] = true, true
	}
	return nil
}

func ensureClients(ctx context.Context, admin *openbao.Client, clientsCAPEM []byte, clients []v1alpha1.OpenBaoServerClient) error {
	if len(clients) == 0 {
		return nil
	}
	mounts, err := admin.SecretMounts(ctx)
	if err != nil {
		return err
	}
	for _, c := range clients {
		if _, ok := mounts[c.KVMount]; !ok {
			if err := admin.EnableKV(ctx, c.KVMount, 2); err != nil {
				return fmt.Errorf("client %s: enabling kv-v2 at %s: %w", c.Name, c.KVMount, err)
			}
		}
		if err := admin.PutPolicy(ctx, ClientPolicy(c.Name), clientPolicyHCL(c.KVMount)); err != nil {
			return fmt.Errorf("client %s: writing its policy: %w", c.Name, err)
		}
		ttl, maxTTL := c.TokenTTL, c.TokenMaxTTL
		if ttl == "" {
			ttl = CoreTokenTTL
		}
		if maxTTL == "" {
			maxTTL = CoreTokenMax
		}
		if err := admin.Write(ctx, "auth/"+CertAuthMount+"/certs/"+c.Name, map[string]any{
			"certificate":          string(clientsCAPEM),
			"allowed_common_names": []string{vaultcert.OpenBaoClientCommonName(c.Name)},
			"token_policies":       []string{ClientPolicy(c.Name)},
			"token_ttl":            ttl,
			"token_max_ttl":        maxTTL,
		}); err != nil {
			return fmt.Errorf("client %s: writing its cert role: %w", c.Name, err)
		}
	}
	return nil
}

type layoutInput struct {
	caPEM        []byte
	clientsCAPEM []byte
	clients      []v1alpha1.OpenBaoServerClient
}

func layoutSummary(in layoutInput) string {
	s := "cert auth, the core policy and kv-v2 at " + KVMount
	for _, c := range in.clients {
		s += fmt.Sprintf("; client %s: policy %s on kv-v2 %s, cert role %s", c.Name, ClientPolicy(c.Name), c.KVMount, c.Name)
	}
	return s
}

func applyLayout(ctx context.Context, admin *openbao.Client, in layoutInput) error {
	if err := ensureLayout(ctx, admin, in.caPEM); err != nil {
		return err
	}
	return ensureClients(ctx, admin, in.clientsCAPEM, in.clients)
}

func ensureLayout(ctx context.Context, admin *openbao.Client, caPEM []byte) error {
	auths, err := admin.AuthMounts(ctx)
	if err != nil {
		return err
	}
	if _, ok := auths[CertAuthMount]; !ok {
		if err := admin.EnableAuth(ctx, CertAuthMount, "cert"); err != nil {
			return fmt.Errorf("enabling cert auth: %w", err)
		}
	}
	if err := admin.PutPolicy(ctx, CorePolicy, corePolicyHCL); err != nil {
		return fmt.Errorf("writing the %s policy: %w", CorePolicy, err)
	}
	if err := admin.Write(ctx, "auth/"+CertAuthMount+"/certs/"+CertAuthRole, map[string]any{
		"certificate":          string(caPEM),
		"allowed_common_names": []string{vaultcert.RootVaultClientCommonName},
		"token_policies":       []string{CorePolicy},
		"token_ttl":            CoreTokenTTL,
		"token_max_ttl":        CoreTokenMax,
	}); err != nil {
		return fmt.Errorf("writing the %s cert role: %w", CertAuthRole, err)
	}
	mounts, err := admin.SecretMounts(ctx)
	if err != nil {
		return err
	}
	if _, ok := mounts[KVMount]; !ok {
		if err := admin.EnableKV(ctx, KVMount, 2); err != nil {
			return fmt.Errorf("enabling kv-v2 at %s: %w", KVMount, err)
		}
	}
	return nil
}

func loginCore(ctx context.Context, c *openbao.Client) (*openbao.Client, error) {
	tok, err := c.LoginCert(ctx, CertAuthMount, CertAuthRole)
	if err != nil {
		return nil, err
	}
	return c.WithToken(tok), nil
}

func loginRefused(err error) bool {
	var re *openbao.ResponseError
	if !errors.As(err, &re) {
		return false
	}
	return re.Status == 400 || re.Status == 403 || re.Status == 404
}
