package openbaoctl

import (
	"context"
	"errors"
	"fmt"

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
