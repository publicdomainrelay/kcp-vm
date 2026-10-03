package mint

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestTheFailureHoldIsBounded(t *testing.T) {
	m := New(failingStore{}, Options{FailureHold: time.Minute})
	now := time.Now()
	ctx := context.Background()

	for i := 0; i < maxHolds*3; i++ {
		_, _ = m.Issue(ctx, DeviceCredential{Token: fmt.Sprintf("junk-%d", i)}, now)
	}

	m.mu.Lock()
	n := len(m.holdTill)
	m.mu.Unlock()

	if n > maxHolds {
		t.Fatalf("the failure hold grew to %d entries after %d distinct junk credentials "+
			"(bound %d). THE KEY IS ATTACKER-CONTROLLED and the endpoint is unauthenticated, "+
			"so this is unbounded memory growth reachable by anyone: a denial of service "+
			"that needs no valid credential and leaves nothing in any log",
			n, maxHolds*3, maxHolds)
	}
}

func TestAnExpiredHoldIsReclaimedRatherThanBlockingAdmission(t *testing.T) {
	m := New(failingStore{}, Options{FailureHold: time.Second})
	ctx := context.Background()
	base := time.Now()

	for i := 0; i < maxHolds; i++ {
		_, _ = m.Issue(ctx, DeviceCredential{Token: fmt.Sprintf("junk-%d", i)}, base)
	}

	_, _ = m.Issue(ctx, DeviceCredential{Token: "after-expiry"}, base.Add(time.Hour))

	m.mu.Lock()
	_, ok := m.holdTill["after-expiry"]
	n := len(m.holdTill)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("a live hold was refused after the map had filled with EXPIRED ones "+
			"(size now %d) -- the sweep exists so the bound reclaims rather than blocks", n)
	}
}

type failingStore struct{}

func (failingStore) Resolve(context.Context, DeviceCredential) (Binding, error) {
	return Binding{}, ErrBindingUnknown
}
func (failingStore) Issue(context.Context, Binding, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, nil
}
