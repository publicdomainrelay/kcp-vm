package tenantclient

import (
	"context"
	"fmt"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
)

func (c *Client) verifyDeviceCredential(ctx context.Context, token string) (string, error) {
	v := &auth.ServiceAuthValidator{Audience: c.opts.Audience, Dir: deviceKeysOnly{}}
	did, err := v.Validate(ctx, token, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", mint.ErrBindingUnknown, err)
	}
	return did.String(), nil
}

type deviceKeysOnly struct{}

var _ identity.Directory = deviceKeysOnly{}

func (deviceKeysOnly) LookupDID(_ context.Context, did syntax.DID) (*identity.Identity, error) {
	pk, err := atcrypto.ParsePublicDIDKey(did.String())
	if err != nil {
		return nil, fmt.Errorf("tenantclient: a device credential must be signed by a did:key: %w", err)
	}
	return &identity.Identity{
		DID: did,
		Keys: map[string]identity.VerificationMethod{
			"atproto": {Type: "Multikey", PublicKeyMultibase: pk.Multibase()},
		},
	}, nil
}

func (deviceKeysOnly) LookupHandle(context.Context, syntax.Handle) (*identity.Identity, error) {
	return nil, fmt.Errorf("tenantclient: a device credential must be signed by a did:key")
}

func (deviceKeysOnly) Lookup(context.Context, syntax.AtIdentifier) (*identity.Identity, error) {
	return nil, fmt.Errorf("tenantclient: a device credential must be signed by a did:key")
}

func (deviceKeysOnly) Purge(context.Context, syntax.AtIdentifier) error { return nil }
