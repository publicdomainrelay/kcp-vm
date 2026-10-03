package revocation

import (
	"context"
	"errors"
	"fmt"
)

type DenyState string

const (
	DenyFirstLogin DenyState = "FirstLogin"

	DenyGranted DenyState = "Granted"

	DenyRevoked DenyState = "Revoked"

	DenyRecordMissing DenyState = "RecordMissing"
)

type Deny interface {
	Lookup(ctx context.Context, did string) (DenyState, error)
}

var (
	ErrRevoked = errors.New("revocation: identity is revoked")

	ErrDenyRecordMissing = errors.New("revocation: account identity record is missing")

	ErrDenyUnreadable = errors.New("revocation: deny record unreadable")

	ErrNoDID = errors.New("revocation: no did")
)

type Permit interface {
	Admitted() string
}

type permit struct{ did string }

func (p *permit) Admitted() string { return p.did }

func Admit(ctx context.Context, d Deny, did string) (Permit, error) {
	if did == "" {
		return nil, ErrNoDID
	}
	st, err := d.Lookup(ctx, did)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %s: %w", ErrDenyUnreadable, did, err)
	case st == DenyRevoked:
		return nil, fmt.Errorf("%w: %s", ErrRevoked, did)
	case st == DenyRecordMissing:
		return nil, fmt.Errorf("%w: %s", ErrDenyRecordMissing, did)
	case st == DenyGranted, st == DenyFirstLogin:
		return &permit{did: did}, nil
	default:
		return nil, fmt.Errorf("%w: %s: unknown deny state %q", ErrDenyUnreadable, did, st)
	}
}

func CreateOnRead(ctx context.Context, d Deny, did string, create func(context.Context) error) (Permit, error) {
	p, err := Admit(ctx, d, did)
	if err != nil {
		return nil, err
	}
	if err := create(ctx); err != nil {
		return nil, err
	}
	return p, nil
}
