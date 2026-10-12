package appcli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

type fakeIdentities struct {
	mu        sync.Mutex
	sequence  []v1alpha1.MarketIdentity
	reads     int
	approved  []string
	rejected  []string
	onApprove func(f *fakeIdentities)
}

func (f *fakeIdentities) GetMarketIdentity(_ context.Context, name string) (*v1alpha1.MarketIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.reads
	if i >= len(f.sequence) {
		i = len(f.sequence) - 1
	}
	f.reads++
	obj := f.sequence[i].DeepCopy()
	obj.Name = name
	return obj, nil
}

func (f *fakeIdentities) ApproveAssociation(_ context.Context, _ string, callerDid string) error {
	f.mu.Lock()
	f.approved = append(f.approved, callerDid)
	hook := f.onApprove
	f.mu.Unlock()
	if hook != nil {
		hook(f)
	}
	return nil
}

func (f *fakeIdentities) RejectAssociation(_ context.Context, _ string, callerDid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejected = append(f.rejected, callerDid)
	return nil
}

func identityIn(phase v1alpha1.MarketIdentityPhase, action *v1alpha1.MarketIdentityPendingAction) v1alpha1.MarketIdentity {
	return v1alpha1.MarketIdentity{Status: v1alpha1.MarketIdentityStatus{Phase: phase, PendingAction: action, Did: "did:plc:bidder", Handle: "bob.test"}}
}

func TestTheRendererShowsTheQrAndReturnsWhenReady(t *testing.T) {
	url := "https://qr.example/#oauth=did%3Aplc%3Acli&n=abc"
	scan := identityIn(v1alpha1.MarketIdentityPhaseAwaitingScan, &v1alpha1.MarketIdentityPendingAction{Kind: v1alpha1.PendingActionScanQr, Url: url, ExpiresAt: "2026-10-12T00:00:00Z"})
	fake := &fakeIdentities{sequence: []v1alpha1.MarketIdentity{scan, scan, scan, identityIn(v1alpha1.MarketIdentityPhaseReady, nil)}}
	var out bytes.Buffer
	r := IdentityRenderer{Client: fake, Out: &out, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := r.WaitReady(ctx, "self")
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if got.Status.Phase != v1alpha1.MarketIdentityPhaseReady {
		t.Fatalf("returned phase %s", got.Status.Phase)
	}
	text := out.String()
	if strings.Count(text, url) != 1 {
		t.Fatalf("the url should be shown once for one pending action, output:\n%s", text)
	}
	if !strings.ContainsAny(text, "\u2580\u2584\u2588") {
		t.Fatalf("no QR blocks rendered:\n%s", text)
	}
	if !strings.Contains(text, "bob.test") {
		t.Fatalf("ready line does not name the handle:\n%s", text)
	}
}

func TestTheRendererApprovesAnAssociationThroughTheSpec(t *testing.T) {
	ask := identityIn(v1alpha1.MarketIdentityPhaseAwaitingApproval, &v1alpha1.MarketIdentityPendingAction{Kind: v1alpha1.PendingActionApproveAssociation, CallerDid: "did:plc:alice", Handle: "alice.test"})
	fake := &fakeIdentities{sequence: []v1alpha1.MarketIdentity{ask}}
	fake.onApprove = func(f *fakeIdentities) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sequence = []v1alpha1.MarketIdentity{identityIn(v1alpha1.MarketIdentityPhaseReady, nil)}
		f.reads = 0
	}
	var out bytes.Buffer
	r := IdentityRenderer{Client: fake, Out: &out, In: strings.NewReader("y\n"), Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.WaitReady(ctx, "self"); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if len(fake.approved) != 1 || fake.approved[0] != "did:plc:alice" || len(fake.rejected) != 0 {
		t.Fatalf("approved %v rejected %v", fake.approved, fake.rejected)
	}
	if !strings.Contains(out.String(), "alice.test") {
		t.Fatalf("prompt does not name the caller:\n%s", out.String())
	}
}

func TestTheRendererRejectsAnAssociationOnNo(t *testing.T) {
	ask := identityIn(v1alpha1.MarketIdentityPhaseAwaitingApproval, &v1alpha1.MarketIdentityPendingAction{Kind: v1alpha1.PendingActionApproveAssociation, CallerDid: "did:plc:mallory"})
	fake := &fakeIdentities{sequence: []v1alpha1.MarketIdentity{ask}}
	var out bytes.Buffer
	r := IdentityRenderer{Client: fake, Out: &out, In: strings.NewReader("n\n"), Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := r.WaitReady(ctx, "self")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady err = %v, want the deadline while the identity is not Ready", err)
	}
	if len(fake.rejected) != 1 || fake.rejected[0] != "did:plc:mallory" || len(fake.approved) != 0 {
		t.Fatalf("approved %v rejected %v", fake.approved, fake.rejected)
	}
}

func TestTheRendererShowsSignInAgainOnSessionExpired(t *testing.T) {
	url := "https://qr.example/#oauth=did%3Aplc%3Acli&n=again"
	expired := identityIn(v1alpha1.MarketIdentityPhaseSessionExpired, &v1alpha1.MarketIdentityPendingAction{Kind: v1alpha1.PendingActionSignInAgain, Url: url})
	fake := &fakeIdentities{sequence: []v1alpha1.MarketIdentity{expired, expired, identityIn(v1alpha1.MarketIdentityPhaseReady, nil)}}
	var out bytes.Buffer
	r := IdentityRenderer{Client: fake, Out: &out, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.WaitReady(ctx, "self"); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if !strings.Contains(out.String(), "expired") || !strings.Contains(out.String(), url) {
		t.Fatalf("no sign-in-again rendering:\n%s", out.String())
	}
}

func TestTheRendererReturnsTheFailureMessage(t *testing.T) {
	failed := v1alpha1.MarketIdentity{Status: v1alpha1.MarketIdentityStatus{Phase: v1alpha1.MarketIdentityPhaseFailed, Message: "pdsAccount needs spec.keysRef"}}
	fake := &fakeIdentities{sequence: []v1alpha1.MarketIdentity{failed}}
	r := IdentityRenderer{Client: fake, Out: &bytes.Buffer{}, Interval: time.Millisecond}
	_, err := r.WaitReady(context.Background(), "self")
	if err == nil || !strings.Contains(err.Error(), "pdsAccount needs spec.keysRef") {
		t.Fatalf("err = %v", err)
	}
}
