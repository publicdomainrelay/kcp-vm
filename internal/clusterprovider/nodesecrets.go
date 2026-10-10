package clusterprovider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

type NodeVault struct {
	connect func() (*openbao.Client, error)

	mu      sync.Mutex
	kvReady bool
}

var _ nodemarket.NodeSecrets = (*NodeVault)(nil)

func NewNodeVault(dataDir string) *NodeVault {
	return NewNodeVaultWith(func() (*openbao.Client, error) {
		seal, err := ReadVaultSeal(dataDir)
		if err != nil {
			return nil, err
		}
		return vaultClient(dataDir, seal.Port, seal.RootToken)
	})
}

func NewNodeVaultWith(connect func() (*openbao.Client, error)) *NodeVault {
	return &NodeVault{connect: connect}
}

func (v *NodeVault) EnsurePolicy(ctx context.Context, node string) error {
	client, err := v.connect()
	if err != nil {
		return err
	}
	if err := client.PutPolicy(ctx, vaultlayout.NodeJWTPolicyName(node), vaultlayout.NodeJWTPolicyHCL(node)); err != nil {
		return fmt.Errorf("clusterprovider: writing policy %s: %w", vaultlayout.NodeJWTPolicyName(node), err)
	}
	return nil
}

func (v *NodeVault) WriteSecret(ctx context.Context, node string, secret nodemarket.NodeSecret) error {
	client, err := v.connect()
	if err != nil {
		return err
	}
	if err := v.ensureKV(ctx, client); err != nil {
		return err
	}
	data := map[string]any{
		"token":            secret.Token,
		"nodePassword":     secret.NodePassword,
		"serverCA":         secret.ServerCA,
		"apiserverTicket":  secret.ApiserverTicket,
		"advertiseAddress": secret.AdvertiseAddress,
		"apiserverPort":    secret.ApiserverPort,
	}
	if err := client.PutKV(ctx, vaultlayout.NodeKVDataPath(node), data); err != nil {
		return fmt.Errorf("clusterprovider: writing %s: %w", vaultlayout.NodeKVDataPath(node), err)
	}
	return nil
}

func (v *NodeVault) ensureKV(ctx context.Context, client *openbao.Client) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.kvReady {
		return nil
	}
	mounts, err := client.SecretMounts(ctx)
	if err != nil {
		return fmt.Errorf("clusterprovider: listing the cluster OpenBao's secret mounts: %w", err)
	}
	if _, ok := mounts[vaultlayout.NodesKVMount]; !ok {
		if err := client.EnableKV(ctx, vaultlayout.NodesKVMount, 2); err != nil {
			return fmt.Errorf("clusterprovider: enabling the %s KV v2 mount: %w", vaultlayout.NodesKVMount, err)
		}
	}
	v.kvReady = true
	return nil
}

func (v *NodeVault) EnsureLogin(ctx context.Context, node string, grant v1alpha1.NodeClaimGrant) (nodemarket.NodeLogin, error) {
	if grant.Issuer == "" || grant.Subject == "" || grant.Audience == "" {
		return nodemarket.NodeLogin{}, fmt.Errorf("clusterprovider: a JWT role for node %s needs an "+
			"issuer, a subject and an audience, and the grant carries %q, %q, %q", node,
			grant.Issuer, grant.Subject, grant.Audience)
	}
	client, err := v.connect()
	if err != nil {
		return nodemarket.NodeLogin{}, err
	}
	mount := vaultlayout.NodeJWTMount(grant.Issuer)
	auths, err := client.AuthMounts(ctx)
	if err != nil {
		return nodemarket.NodeLogin{}, fmt.Errorf("clusterprovider: listing the cluster OpenBao's auth mounts: %w", err)
	}
	if existing, ok := auths[mount]; !ok {
		if err := client.EnableAuth(ctx, mount, "jwt"); err != nil {
			return nodemarket.NodeLogin{}, fmt.Errorf("clusterprovider: enabling the jwt auth mount %s: %w", mount, err)
		}
	} else if existing.Type != "jwt" {
		return nodemarket.NodeLogin{}, fmt.Errorf("clusterprovider: auth mount %s exists with type %q, "+
			"not jwt, so no node of issuer %s can log in through it", mount, existing.Type, grant.Issuer)
	}
	config, err := client.Read(ctx, "auth/"+mount+"/config")
	if err != nil && !errors.Is(err, openbao.ErrNotFound) {
		return nodemarket.NodeLogin{}, fmt.Errorf("clusterprovider: reading auth/%s/config: %w", mount, err)
	}
	if config["oidc_discovery_url"] != grant.Issuer || config["bound_issuer"] != grant.Issuer {
		if err := client.Write(ctx, "auth/"+mount+"/config", map[string]any{
			"oidc_discovery_url": grant.Issuer,
			"bound_issuer":       grant.Issuer,
		}); err != nil {
			return nodemarket.NodeLogin{}, fmt.Errorf("clusterprovider: configuring auth/%s with the "+
				"discovery document of %s: %w -- OpenBao fetches that document when the config is "+
				"written, so the bidder's issuer must answer from this host", mount, grant.Issuer, err)
		}
	}
	role := vaultlayout.NodeJWTRoleName(node)
	policy := vaultlayout.NodeJWTPolicyName(node)
	if err := client.Write(ctx, "auth/"+mount+"/role/"+role, map[string]any{
		"role_type":       vaultlayout.NodeJWTRoleType,
		"bound_subject":   grant.Subject,
		"bound_audiences": []string{grant.Audience},
		"user_claim":      vaultlayout.NodeJWTUserClaim,
		"token_ttl":       vaultlayout.NodeJWTTokenTTL,
		"token_policies":  []string{policy},
	}); err != nil {
		return nodemarket.NodeLogin{}, fmt.Errorf("clusterprovider: writing role %s under auth/%s: %w", role, mount, err)
	}
	return nodemarket.NodeLogin{Mount: mount, Role: role, Policy: policy}, nil
}

func (v *NodeVault) Revoke(ctx context.Context, node string) error {
	client, err := v.connect()
	if err != nil {
		return err
	}
	var problems []error
	auths, err := client.AuthMounts(ctx)
	if err != nil {
		problems = append(problems, fmt.Errorf("clusterprovider: listing auth mounts to find node "+
			"%s's role: %w", node, err))
	}
	mounts := make([]string, 0, len(auths))
	for name, m := range auths {
		if m.Type == "jwt" && vaultlayout.IsNodeJWTMount(name) {
			mounts = append(mounts, name)
		}
	}
	sort.Strings(mounts)
	role := vaultlayout.NodeJWTRoleName(node)
	for _, mount := range mounts {
		if err := client.Delete(ctx, "auth/"+mount+"/role/"+role); err != nil {
			problems = append(problems, fmt.Errorf("clusterprovider: deleting role %s under auth/%s: %w",
				role, mount, err))
		}
	}
	if err := client.DeletePolicy(ctx, vaultlayout.NodeJWTPolicyName(node)); err != nil {
		problems = append(problems, fmt.Errorf("clusterprovider: deleting policy %s: %w",
			vaultlayout.NodeJWTPolicyName(node), err))
	}
	if err := client.Delete(ctx, vaultlayout.NodeKVMetadataPath(node)); err != nil {
		problems = append(problems, fmt.Errorf("clusterprovider: deleting %s: %w",
			vaultlayout.NodeKVMetadataPath(node), err))
	}
	return errors.Join(problems...)
}
