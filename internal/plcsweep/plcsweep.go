package plcsweep

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

const DefaultInterval = time.Hour

type Worklist interface {
	DIDs(ctx context.Context) ([]string, error)
}

type Resolver interface {
	Resolve(ctx context.Context, did string) (revocation.PLCResolution, error)
}

type Revoker interface {
	Revoke(ctx context.Context, did, why string) error
}

type Sweeper struct {
	Worklist Worklist
	Resolver Resolver
	Revoker  Revoker
}

type Outcome struct {
	DID    string
	Revoke bool
	Why    string

	Skipped bool
}

func (s *Sweeper) Pass(ctx context.Context) ([]Outcome, error) {
	dids, err := s.Worklist.DIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("plcsweep: listing the worklist: %w", err)
	}
	out := make([]Outcome, 0, len(dids))
	for _, did := range dids {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res, err := s.Resolver.Resolve(ctx, did)
		if err != nil {
			out = append(out, Outcome{DID: did, Skipped: true, Why: err.Error()})
			continue
		}
		revoke, why := revocation.Sweep(res)
		if !revoke {
			out = append(out, Outcome{DID: did, Why: why})
			continue
		}
		if err := s.Revoker.Revoke(ctx, did, why); err != nil {
			out = append(out, Outcome{DID: did, Revoke: true, Why: why + " (DENY NOT WRITTEN: " + err.Error() + ")"})
			continue
		}
		out = append(out, Outcome{DID: did, Revoke: true, Why: why})
	}
	return out, nil
}

var ErrNoInterval = errors.New("plcsweep: interval must be positive")

func (s *Sweeper) Run(ctx context.Context, interval time.Duration, onPass func([]Outcome)) error {
	if interval <= 0 {
		return ErrNoInterval
	}
	if err := s.once(ctx, onPass); err != nil && ctx.Err() == nil {
		onPass([]Outcome{{Why: "pass failed: " + err.Error(), Skipped: true}})
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.once(ctx, onPass); err != nil && ctx.Err() == nil {
				onPass([]Outcome{{Why: "pass failed: " + err.Error(), Skipped: true}})
			}
		}
	}
}

func (s *Sweeper) once(ctx context.Context, onPass func([]Outcome)) error {
	out, err := s.Pass(ctx)
	if onPass != nil && len(out) > 0 {
		onPass(out)
	}
	return err
}
