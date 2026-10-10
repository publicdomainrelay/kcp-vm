package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/bluesky-social/indigo/atproto/identity"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/plcsweep"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/xrpcserver"
)

type Deps struct {
	Store gateway.Store
	Deny  revocation.Deny

	Directory identity.Directory

	Revoker gateway.Revoker

	MintStore mint.Store

	Sweep *plcsweep.Sweeper

	SweepInterval time.Duration
}

var ErrNoMintStore = errors.New(
	"no mint.Store was supplied, so neither /xrpc/* nor /v1/credential can be served. " +
		"internal/tenantclient is the implementation and main.buildDeps builds it from " +
		"SOCIALWEB_GATEWAY_DEVKEY_REGISTRY and SOCIALWEB_GATEWAY_MINT_KUBECONFIGS -- a " +
		"directory of per-cluster kubeconfigs whose identity is that cluster's `gwmint` " +
		"ServiceAccount (deploy/tenant-grant.yaml). A nil here means the composition root " +
		"skipped that construction, not that something is missing from the repo")

type Gateway struct {
	handler       http.Handler
	sweep         *plcsweep.Sweeper
	sweepInterval time.Duration
	log           *slog.Logger
}

func Build(cfg Config, d Deps, log *slog.Logger) (*Gateway, error) {
	if d.MintStore == nil {
		return nil, ErrNoMintStore
	}
	if log == nil {
		log = slog.Default()
	}

	verbs := &gateway.Server{
		Deny: d.Deny, Store: d.Store, Revoker: d.Revoker,
		MaxDeviceKeyTTL: cfg.DeviceKeyMaxTTL,
	}
	credentials := mint.New(d.MintStore, mint.Options{TTL: cfg.MintTTL, FailureHold: cfg.MintFailureHold})

	surface, err := xrpcserver.New(xrpcserver.Options{
		Gateway:   verbs,
		Mint:      credentials,
		Audiences: cfg.Audiences,
		Directory: d.Directory,
	})
	if err != nil {
		return nil, err
	}

	interval := d.SweepInterval
	if interval == 0 {
		interval = plcsweep.DefaultInterval
	}
	g := &Gateway{
		handler:       surface.Handler(),
		sweep:         d.Sweep,
		sweepInterval: interval,
		log:           log,
	}
	g.report(d)
	return g, nil
}

func (g *Gateway) Handler() http.Handler { return g.handler }

func (g *Gateway) Run(ctx context.Context) error {
	if g.sweep == nil {
		<-ctx.Done()
		return nil
	}
	err := g.sweep.Run(ctx, g.sweepInterval, g.onPass)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

func (g *Gateway) onPass(outcomes []plcsweep.Outcome) {
	for _, o := range outcomes {
		switch {
		case o.Skipped:
			g.log.Warn("plc sweep: did not resolve", "did", o.DID, "why", o.Why)
		case o.Revoke:
			g.log.Info("plc sweep: deny written", "did", o.DID, "why", o.Why)
		default:
			g.log.Debug("plc sweep: live", "did", o.DID, "why", o.Why)
		}
	}
}

func (g *Gateway) report(d Deps) {
	if g.sweep == nil {
		g.log.Warn("the PLC sweep is UNWIRED",
			"missing", "Deps.Sweep: plcsweep.Worklist, plcsweep.Resolver and plcsweep.Revoker are all implemented, and main.buildDeps builds and wires all three. This warning means the composition root skipped that construction, so g.Run -- the only caller of sweep.Run -- has nothing to run",
			"reached by", "a caller that supplies a nil Deps.Sweep, which today is the wiring smoke tests ONLY",
			"consequence", "an account deleted at PLC between logins is not noticed by this process")
	}
	if d.Revoker == nil {
		g.log.Warn("revokeIdentity is routed but UNWRITABLE",
			"missing", "gateway.Revoker -- the deny's write half has no implementation",
			"consequence", "revokeIdentity answers 503 ClusterUnavailable")
	}
}
