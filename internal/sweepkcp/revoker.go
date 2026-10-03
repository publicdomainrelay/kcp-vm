package sweepkcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

type DenyWriter interface {
	WriteDeny(ctx context.Context, did string) error
}

type FanOut interface {
	FanOut(ctx context.Context, did string) ([]revocation.ClusterView, error)
}

type Eraser interface {
	DeleteServiceAccount(ctx context.Context, did, cluster string) error
}

var errNoEraser = errors.New(
	"the deny IS written, but no Eraser is wired: the ServiceAccount cannot be removed in this " +
		"build, so every cluster that still holds one stays RevocationPending")

type Plan struct {
	DID string

	Why string

	Entries []revocation.ReplyEntry

	Err error
}

type Revoker struct {
	Deny DenyWriter

	FanOut FanOut

	Erase Eraser

	OnPlan func(Plan)
}

func (r *Revoker) Revoke(ctx context.Context, did, why string) error {
	if r.Deny == nil {
		return errors.New("sweepkcp: no DenyWriter: the deny's write half has no implementation, " +
			"and a sweep that cannot write the deny must not report a revocation")
	}
	if err := r.Deny.WriteDeny(ctx, did); err != nil {
		return fmt.Errorf("sweepkcp: write the deny for %s: %w", did, err)
	}

	plan := Plan{DID: did, Why: why}
	switch {
	case r.FanOut == nil:
		plan.Err = errors.New("no FanOut: whether the identity's ServiceAccount still exists in " +
			"each cluster is not observable in this build, so the fan-out is unwritten by anything " +
			"this process can see")
	default:
		views, err := r.FanOut.FanOut(ctx, did)
		if err != nil {
			plan.Err = fmt.Errorf("the deny IS written; the fan-out could not be observed: %w", err)
		} else {
			effects, err := r.erase(ctx, did, revocation.Plan(views))
			if err != nil {
				plan.Err = err
			}
			plan.Entries = revocation.Reply(effects)
		}
	}
	if r.OnPlan != nil {
		r.OnPlan(plan)
	}
	return nil
}

func (r *Revoker) erase(ctx context.Context, did string, effects []revocation.Effect) ([]revocation.Effect, error) {
	if r.Erase == nil {
		for _, e := range effects {
			if e.DeleteSA {
				return effects, errNoEraser
			}
		}
		return effects, nil
	}
	applied := make([]revocation.Effect, 0, len(effects))
	for _, e := range effects {
		if e.DeleteSA {
			if err := r.Erase.DeleteServiceAccount(ctx, did, e.Cluster); err != nil {
				applied = append(applied, e)
				continue
			}
			e.DeleteSA = false
			e.State = v1alpha1.IdentityRevoked
		}
		applied = append(applied, e)
	}
	return applied, nil
}
