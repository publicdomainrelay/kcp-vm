package mint

import (
	"context"
	"errors"
	"sync"
	"time"
)

type ExecCredential struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Spec       ExecCredentialSpec `json:"spec"`
	Status     ExecCredentialData `json:"status"`
}

type ExecCredentialSpec struct {
	Interactive bool `json:"interactive"`
}

type ExecCredentialData struct {
	Token string `json:"token"`

	ExpirationTimestamp string `json:"expirationTimestamp"`
}

const (
	APIVersion = "client.authentication.k8s.io/v1"
	Kind       = "ExecCredential"

	RenewBefore = 30 * time.Second
)

type DeviceCredential struct {
	Token string
}

type Binding struct {
	DID      string
	Cluster  string
	SA       string
	SAExists bool
	Instance string
	Running  bool
}

var ErrBindingUnknown = errors.New("mint: unknown device credential")

var ErrServiceAccountGone = errors.New("mint: service account no longer exists")

type Store interface {
	Resolve(ctx context.Context, cred DeviceCredential) (Binding, error)

	Issue(ctx context.Context, b Binding, ttl time.Duration) (token string, expires time.Time, err error)
}

type Options struct {
	TTL time.Duration

	FailureHold time.Duration
}

const (
	DefaultTTL         = 15 * time.Minute
	DefaultFailureHold = 60 * time.Second
)

type Mint struct {
	Store Store
	Opts  Options

	mu       sync.Mutex
	holdTill map[string]time.Time
}

func New(store Store, opts Options) *Mint {
	if opts.TTL == 0 {
		opts.TTL = DefaultTTL
	}
	if opts.FailureHold == 0 {
		opts.FailureHold = DefaultFailureHold
	}
	return &Mint{Store: store, Opts: opts, holdTill: map[string]time.Time{}}
}

func (m *Mint) Issue(ctx context.Context, cred DeviceCredential, now time.Time) (*ExecCredential, error) {
	if cred.Token == "" {
		return nil, ErrBindingUnknown
	}
	if m.onHold(cred.Token, now) {
		return nil, ErrHeld
	}

	b, err := m.Store.Resolve(ctx, cred)
	if err != nil {
		m.hold(cred.Token, now)
		return nil, err
	}

	if !b.SAExists {
		m.hold(cred.Token, now)
		return nil, ErrServiceAccountGone
	}
	if b.Instance == "" {
		m.hold(cred.Token, now)
		return nil, ErrBindingUnknown
	}

	token, expires, err := m.Store.Issue(ctx, b, m.Opts.TTL)
	if err != nil {
		m.hold(cred.Token, now)
		return nil, err
	}
	if token == "" {
		m.hold(cred.Token, now)
		return nil, errors.New("mint: store returned an empty token")
	}

	renewAt := expires.Add(-RenewBefore)
	if !renewAt.After(now) {
		renewAt = now.Add(m.Opts.TTL / 2)
	}
	return &ExecCredential{
		APIVersion: APIVersion,
		Kind:       Kind,
		Spec:       ExecCredentialSpec{Interactive: false},
		Status: ExecCredentialData{
			Token:               token,
			ExpirationTimestamp: renewAt.UTC().Format(time.RFC3339),
		},
	}, nil
}

var ErrHeld = errors.New("mint: credential is in its failure hold")

func (m *Mint) onHold(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.holdTill[key]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(m.holdTill, key)
		return false
	}
	return true
}

const maxHolds = 10_000

func (m *Mint) hold(key string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.holdTill) >= maxHolds {
		m.sweepLocked(now)
		if len(m.holdTill) >= maxHolds {
			return
		}
	}
	m.holdTill[key] = now.Add(m.Opts.FailureHold)
}

func (m *Mint) sweepLocked(now time.Time) {
	for k, until := range m.holdTill {
		if now.After(until) {
			delete(m.holdTill, k)
		}
	}
}
