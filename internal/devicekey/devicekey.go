package devicekey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kubeconfig"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tenantclient"
)

type Binder interface {
	Record(ctx context.Context, r tenantclient.Row) error
}

type Options struct {
	Binder Binder

	Audience string

	TTL time.Duration

	ExecCommand string
	ExecEnvName string
}

type Granter struct {
	opts Options
}

var _ kcpclient.DeviceKeyGranter = (*Granter)(nil)

func New(opts Options) (*Granter, error) {
	switch {
	case opts.Binder == nil:
		return nil, errors.New("devicekey: Binder is required: an unbound key resolves to nothing")
	case opts.Audience == "":
		return nil, errors.New("devicekey: Audience is required: the credential must be signed for the audience the mint checks")
	case opts.TTL <= 0:
		return nil, errors.New("devicekey: TTL is required and must be positive: a device credential's lifetime is a policy decision, not a default")
	case opts.ExecCommand == "":
		return nil, errors.New("devicekey: ExecCommand is required: the kubeconfig's exec block names the helper a user runs")
	case opts.ExecEnvName == "":
		return nil, errors.New("devicekey: ExecEnvName is required: a private key handed to the helper needs a variable name, and it is not derivable")
	}
	return &Granter{opts: opts}, nil
}

func (g *Granter) Grant(ctx context.Context, in kcpclient.GrantInput) (string, error) {
	if in.Caller == "" || in.Cluster == "" || in.Server == "" {
		return "", errors.New("devicekey: a grant needs a verified caller, a cluster and a server")
	}

	priv, err := atcrypto.GeneratePrivateKeyP256()
	if err != nil {
		return "", fmt.Errorf("devicekey: generate the device key: %w", err)
	}
	pub, err := priv.PublicKey()
	if err != nil {
		return "", fmt.Errorf("devicekey: the device key has no public half: %w", err)
	}
	deviceKey := pub.DIDKey()

	if err := g.opts.Binder.Record(ctx, tenantclient.Row{
		DeviceKey: deviceKey,
		DID:       string(in.Caller),
		Cluster:   in.Cluster,
	}); err != nil {
		return "", fmt.Errorf("devicekey: bind %s: %w", deviceKey, err)
	}

	credential, err := auth.SignServiceAuth(syntax.DID(deviceKey), g.opts.Audience, g.opts.TTL, nil, priv)
	if err != nil {
		return "", fmt.Errorf("devicekey: sign the device credential: %w", err)
	}

	rendered, err := kubeconfig.Build(kubeconfig.Input{
		ClusterName:      in.Cluster,
		Server:           in.Server,
		ExecCommand:      g.opts.ExecCommand,
		DeviceCredential: credential,
		DeviceKeyEnvName: g.opts.ExecEnvName,
	})
	if err != nil {
		return "", err
	}
	if err := kubeconfig.AssertClean(rendered); err != nil {
		return "", err
	}
	return rendered, nil
}
